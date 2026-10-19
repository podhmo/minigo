// Package resolve maps import paths and directories to a package's
// directory and build file list, without resolving the package's own
// imports.
//
// Start with [NewGoScanResolver]: its [GoScanResolver.Locate] and
// [GoScanResolver.LocateDir] implement [Resolver] and return a
// [PackageMeta]. [BuildConfig] picks the target platform and build tags,
// and its AllowedRoots field limits which directories may be handed out
// ([BuildConfig.CheckDir]). An import path that resolves to nothing
// reports [ErrUnresolved].
//
// Lower-level helpers work without a resolver: [ReadPackageFiles] and
// [FileScopeFiles] list the files of one directory, [ScanRepo] enumerates
// every package under a root as [RepoPackage] values, and [ModuleLang],
// [ModulePath] and [RequiredVersion] read the enclosing go.mod.
//
// [github.com/podhmo/minigo.Engine.WithResolver] installs a custom
// [Resolver]; the engine indexes the located files with
// [github.com/podhmo/minigo/index].
package resolve

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/pkg/locator"
)

// BuildConfig selects which files belong to a package build and which
// directories a [Resolver] may enter. The zero value targets the host
// platform with no extra tags and no directory restriction.
type BuildConfig struct {
	GOOS   string
	GOARCH string
	Tags   []string

	// AllowedRoots, when non-empty, restricts which directories a Resolver
	// may hand out: a located package directory must live inside one of the
	// roots (or be the root itself).
	AllowedRoots []string
}

// CheckDir reports an error when dir lies outside every AllowedRoots
// entry; it always succeeds when AllowedRoots is empty. Symlinks on both
// sides are resolved first, so a symlink inside a root pointing outside it
// fails the check. dir need not exist: its nearest existing ancestor is
// resolved instead.
func (cfg BuildConfig) CheckDir(dir string) error {
	if len(cfg.AllowedRoots) == 0 {
		return nil
	}
	abs, err := resolveSymlinkNearest(dir)
	if err != nil {
		return err
	}
	for _, root := range cfg.AllowedRoots {
		r, err := resolveSymlinkNearest(root)
		if err != nil {
			continue
		}
		if abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("directory %s is outside the allowed roots", abs)
}

// CheckPath is [BuildConfig.CheckDir] for a path that may name a file,
// such as a write target.
func (cfg BuildConfig) CheckPath(path string) error {
	return cfg.CheckDir(path)
}

// resolveSymlinkNearest absolutizes path and resolves symlinks as far as
// the filesystem allows: the path itself when it exists, otherwise the
// longest existing ancestor is resolved and the remaining tail re-joined.
func resolveSymlinkNearest(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	var tail []string
	dir := abs
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil // reached the filesystem root unresolved
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Join(parts...), nil
		}
	}
}

// PackageMeta is a package's name and file list, read before any file is
// fully parsed. A [Resolver] or [ReadPackageFiles] returns it.
type PackageMeta struct {
	ImportPath string
	Name       string // the package clause name (may differ from path basename)
	Dir        string
	GoFiles    []string // sorted, filtered by build constraints, cgo files skipped
	Standard   bool     // inside GOROOT
	ModulePath string   // module path when known, "" otherwise
	// Lang is the `go` directive of the module enclosing Dir (raw text like
	// "1.26.0"); "" when the directory is outside any module. Each file's
	// effective -lang still honors its own //go:build version constraint.
	Lang string
}

// ErrUnresolved matches, under errors.Is, an error from
// [GoScanResolver.Locate] when no directory is found for the import path;
// the error message is not changed. A package that is found but cannot be
// read (no buildable Go files, a broken replace directive) does not match, so callers can tell "nothing there" from "there but broken".
// A missing directory passed to [GoScanResolver.LocateDir] matches
// fs.ErrNotExist instead.
var ErrUnresolved = errors.New("resolve: no such package")

// unresolved marks a locate miss for errors.Is without changing the
// reported message.
type unresolved struct{ err error }

func (e unresolved) Error() string { return e.err.Error() }
func (e unresolved) Unwrap() error { return e.err }
func (e unresolved) Is(target error) bool {
	return target == ErrUnresolved
}

// markUnresolved wraps a locate failure as ErrUnresolved; the message
// is unchanged. Only a miss is marked — a resolved-but-broken module
// graph (locator.ErrBrokenConfig: a replace cycle, an unspellable
// replace target) passes through unmarked so the Lookup* soft loaders
// keep "found but broken" loud instead of answering nil.
func markUnresolved(err error) error {
	if errors.Is(err, locator.ErrBrokenConfig) {
		return err
	}
	return unresolved{err}
}

// Resolver locates a package. Implementations must not resolve the
// package's own imports; the caller locates each import when it needs it.
// [NewGoScanResolver] returns the default implementation.
type Resolver interface {
	// Locate resolves an import path (e.g. "fmt", "github.com/x/y").
	// fromDir is the importing package's directory, or "" when there is
	// none.
	Locate(ctx context.Context, fromDir, importPath string) (*PackageMeta, error)
	// LocateDir resolves a filesystem directory (e.g. "./app") — used for
	// the entry package, which need not have an import path.
	LocateDir(ctx context.Context, dir string) (*PackageMeta, error)
}

// ReadPackageFiles lists the files of the package in dir as
// importPath, without a [Resolver]. Files are filtered with go/build's
// match rules for cfg (//go:build constraints, _GOOS/_GOARCH suffixes,
// _test.go); files importing "C" are skipped, as cgo is not supported.
// Name is the package clause most of the files declare. It reports an
// error when go/build cannot read a candidate file, or when no buildable
// file remains. ModulePath is left empty. cfg.AllowedRoots is not checked.
func ReadPackageFiles(dir, importPath string, cfg BuildConfig) (*PackageMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading package dir %s: %w", dir, err)
	}
	ctx := build.Default
	if cfg.GOOS != "" {
		ctx.GOOS = cfg.GOOS
	}
	if cfg.GOARCH != "" {
		ctx.GOARCH = cfg.GOARCH
	}
	ctx.BuildTags = append(ctx.BuildTags, cfg.Tags...)

	var files []string
	var rejected []string // files MatchFile could not even read/parse
	excluded := 0         // files build constraints filtered out
	cgo := 0              // matched files skipped for importing "C"
	clauses := map[string]int{}
	clauseOrder := []string{} // first-seen; os.ReadDir is sorted by name
	fset := token.NewFileSet()
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") ||
			strings.HasPrefix(fname, "_") || strings.HasPrefix(fname, ".") {
			// _- and .-prefixed files are invisible to the Go build
			// system entirely — MatchFile would reject them by NAME,
			// not by constraint, so they must not inflate excluded.
			continue
		}
		match, err := ctx.MatchFile(dir, fname)
		if err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		if !match {
			excluded++
			continue
		}
		abs := filepath.Join(dir, fname)
		if f, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly); err == nil && f != nil {
			// go/build only lists cgo files under CgoEnabled, and the
			// interpreter cannot link native code, so a directory walk
			// skips them like ignored files (an explicitly named file
			// still fails at index time).
			if importsC(f) {
				cgo++
				continue
			}
			if clauses[f.Name.Name] == 0 {
				clauseOrder = append(clauseOrder, f.Name.Name)
			}
			clauses[f.Name.Name]++
		}
		files = append(files, abs)
	}
	if len(rejected) > 0 {
		// A file MatchFile cannot even read or parse would vanish from
		// the index with no other error channel — the package then
		// reads as complete while missing decls and import edges.
		// `go build` fails on the same files, so fail here and name
		// them rather than building a partial package.
		return nil, fmt.Errorf("reading package dir %s: %s", dir, strings.Join(rejected, "; "))
	}
	if len(files) == 0 {
		// "no buildable Go source files" alone cannot tell a permission
		// problem from a //go:build exclusion — name the package and why.
		what := dir
		if importPath != "" && !strings.HasPrefix(importPath, "<") {
			what = fmt.Sprintf("package %s (%s)", importPath, dir)
		}
		var reasons []string
		if excluded > 0 {
			reasons = append(reasons, fmt.Sprintf("%d excluded by build constraints", excluded))
		}
		if cgo > 0 {
			reasons = append(reasons, fmt.Sprintf("%d skipped (cgo is not supported)", cgo))
		}
		if len(reasons) > 0 {
			return nil, fmt.Errorf("no buildable Go source files in %s: %s", what, strings.Join(reasons, ", "))
		}
		return nil, fmt.Errorf("no buildable Go source files in %s", what)
	}
	// The package clause is a majority vote over every matched file —
	// a mixed directory (already a `go build` failure) must not take
	// its canonical name from whichever file sorts first, which can be
	// the minority clause. Ties keep the alphabetically-first clause.
	name := ""
	best := 0
	for _, c := range clauseOrder {
		if clauses[c] > best {
			best, name = clauses[c], c
		}
	}
	sort.Strings(files)
	return &PackageMeta{
		ImportPath: importPath,
		Name:       name,
		Dir:        dir,
		GoFiles:    files,
		Standard:   strings.HasPrefix(dir, build.Default.GOROOT),
		Lang:       ModuleLang(dir),
	}, nil
}

// importsC reports whether the parsed file imports "C", the cgo
// pseudo-package (any import spelling — import "C", import C "C",
// import _ "C").
func importsC(f *ast.File) bool {
	for _, is := range f.Imports {
		if path, err := strconv.Unquote(is.Path.Value); err == nil && path == "C" {
			return true
		}
	}
	return false
}

// LooksLikeDir reports whether path names a filesystem location rather
// than an import path: it starts with "." or is absolute.
func LooksLikeDir(path string) bool {
	return strings.HasPrefix(path, ".") || filepath.IsAbs(path)
}
