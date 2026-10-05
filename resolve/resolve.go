// Package resolve maps import paths to package directories and file lists,
// without expanding the import graph. The default backend adapts go-scan's
// lazy locator; a `go list -find` backend may be added as an opt-in.
package resolve

import (
	"context"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BuildConfig selects which files belong to a package build.
type BuildConfig struct {
	GOOS   string
	GOARCH string
	Tags   []string

	// AllowedRoots, when non-empty, restricts which directories a Resolver
	// may hand out: a located package directory must live inside one of the
	// roots (or be the root itself). This is the "which directories may the
	// interpreter enter" knob — e.g. a REPL's AllowedRoots is its CWD.
	AllowedRoots []string
}

// CheckDir verifies dir against AllowedRoots (no-op when unrestricted).
// Both sides are resolved through EvalSymlinks so a symlink inside a root
// pointing outside it cannot bypass the check.
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

// CheckPath verifies an arbitrary filesystem path against AllowedRoots
// (no-op when unrestricted). Unlike CheckDir the path need not exist:
// symlinks are resolved through the nearest existing ancestor so a write
// target under a symlinked directory cannot escape the roots either.
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

// PackageMeta is the cheap metadata level of a package — enough to know its
// real name and which files belong to it, before any AST is built.
type PackageMeta struct {
	ImportPath string
	Name       string // the package clause name (may differ from path basename)
	Dir        string
	GoFiles    []string // absolute paths, filtered by build constraints
	Standard   bool     // inside GOROOT
	ModulePath string   // owning module path, "" if unknown
	// Lang is the `go` directive of the module enclosing Dir (raw text like
	// "1.26.0"); "" when the directory is outside any module. Each file's
	// effective -lang still honors its own //go:build version constraint.
	Lang string
}

// Resolver locates a package. It must NOT recursively resolve the package's
// own imports — that is the point of the lazy design.
type Resolver interface {
	// Locate resolves an import path (e.g. "fmt", "github.com/x/y").
	Locate(ctx context.Context, fromDir, importPath string) (*PackageMeta, error)
	// LocateDir resolves a filesystem directory (e.g. "./app") — used for
	// the entry package, which need not have an import path.
	LocateDir(ctx context.Context, dir string) (*PackageMeta, error)
}

// ReadPackageFiles reads a directory into a PackageMeta: it filters files
// with go/build's match rules (//go:build constraints, _GOOS/_GOARCH
// suffixes, _test.go) and reads the package clause of the first match.
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
	var name string
	var rejected []string // files MatchFile could not even read/parse
	excluded := 0         // files build constraints filtered out
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		match, err := ctx.MatchFile(dir, e.Name())
		if err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		if !match {
			excluded++
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
		if name == "" {
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly)
			if err == nil && f != nil {
				name = f.Name.Name
			}
		}
	}
	if len(files) == 0 {
		// "no buildable Go source files" alone cannot tell a permission
		// problem from a //go:build exclusion — name the package and why.
		what := dir
		if importPath != "" && !strings.HasPrefix(importPath, "<") {
			what = fmt.Sprintf("package %s (%s)", importPath, dir)
		}
		switch {
		case len(rejected) > 0:
			return nil, fmt.Errorf("no buildable Go source files in %s: %s", what, strings.Join(rejected, "; "))
		case excluded > 0:
			return nil, fmt.Errorf("no buildable Go source files in %s: all %d .go file(s) excluded by build constraints", what, excluded)
		default:
			return nil, fmt.Errorf("no buildable Go source files in %s", what)
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

// CheckImportPath sanity-checks a path that claims to be an import path.
func LooksLikeDir(path string) bool {
	return strings.HasPrefix(path, ".") || filepath.IsAbs(path)
}

var _ = ast.IsExported // keep go/ast imported for future TypeRef work
