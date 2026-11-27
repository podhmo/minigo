package resolve

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/podhmo/minigo/pkg/locator"
)

// GoScanResolver is the default [Resolver]. It finds import paths through
// the main module's go.mod (including replace directives), GOROOT and the
// module cache, without running the go command.
type GoScanResolver struct {
	// Backed by go-scan's locator (pkg/locator).
	loc *locator.Locator
	cfg BuildConfig
}

// RootDir returns the directory of the anchoring go.mod, or startDir when
// there is none.
func (r *GoScanResolver) RootDir() string { return r.loc.RootDir() }

// ModulePath returns the anchoring module's path, or "" when there is no
// go.mod.
func (r *GoScanResolver) ModulePath() string { return r.loc.ModulePath() }

// Requires returns a copy of the go.mod require directives, keyed by
// module path.
func (r *GoScanResolver) Requires() map[string]string { return r.loc.Requires() }

// Locate implements [Resolver] for an import path, searched from fromDir;
// use [GoScanResolver.LocateDir] for a directory or a .go file. When no
// directory is found the error matches [ErrUnresolved]; a directory
// outside cfg.AllowedRoots is an error too.
func (r *GoScanResolver) Locate(ctx context.Context, fromDir, importPath string) (*PackageMeta, error) {
	dir, err := r.loc.FindPackageDirFrom(fromDir, importPath)
	if err != nil {
		return nil, fmt.Errorf("resolving import %q: %w", importPath, markUnresolved(err))
	}
	if err := r.cfg.CheckDir(dir); err != nil {
		return nil, err
	}
	return ReadPackageFiles(dir, importPath, r.cfg)
}

// LocateDir implements [Resolver] for a directory rather than an import
// path. The import path is derived from the anchoring module; a directory
// it cannot name gets the synthetic path "<dir>" followed by the absolute
// directory. dir may also name a .go file: like `go run file.go`, that
// file alone forms the package, build constraints are ignored, and the
// import path starts with [FileImportPrefix]. A directory or file outside
// cfg.AllowedRoots is an error.
func (r *GoScanResolver) LocateDir(ctx context.Context, dir string) (*PackageMeta, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := r.cfg.CheckDir(abs); err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("entry %q not found: %w", dir, err)
	}
	if !st.IsDir() {
		if strings.HasSuffix(abs, ".go") {
			return r.locateFile(abs)
		}
		return nil, fmt.Errorf("entry %q is neither a directory nor a .go file", dir)
	}
	importPath, err := r.loc.PathToImport(abs)
	if err != nil || importPath == "" {
		importPath = "<dir>" + abs
	}
	meta, err := ReadPackageFiles(abs, importPath, r.cfg)
	if err != nil {
		return nil, err
	}
	meta.ModulePath = r.loc.ModulePath()
	return meta, nil
}

// locateFile makes a one-file package out of a .go file, like `go run
// file.go`: sibling files are not read and build constraints are ignored
// (the `//go:build ignore` generator pattern). The import path is
// synthetic, so the file never stands in for its directory's package.
func (r *GoScanResolver) locateFile(abs string) (*PackageMeta, error) {
	f, err := parser.ParseFile(token.NewFileSet(), abs, nil, parser.PackageClauseOnly)
	if err != nil {
		return nil, fmt.Errorf("reading package clause: %w", err)
	}
	dir := filepath.Dir(abs)
	return &PackageMeta{
		ImportPath: FileImportPrefix + abs,
		Name:       f.Name.Name,
		Dir:        dir,
		GoFiles:    []string{abs},
		ModulePath: r.loc.ModulePath(),
		Lang:       ModuleLang(dir),
	}, nil
}

// LocateFiles resolves a go-run-style list of named .go files into a
// single package meta, like `go run a.go b.go`: every file must live
// in one directory and declare the same package, duplicates are
// rejected, and build constraints are ignored. The import path is the
// synthetic [FileImportPrefix] spelling of the shared directory, so
// the file set never stands in for the directory's package.
func (r *GoScanResolver) LocateFiles(ctx context.Context, files []string) (*PackageMeta, error) {
	var dir, name string
	seen := map[string]bool{}
	var gofiles []string
	for _, file := range files {
		abs, err := filepath.Abs(file)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(abs, ".go") {
			return nil, fmt.Errorf("named files must be .go files: %s", file)
		}
		// the root check and stat read the link's target — a .go
		// symlink inside an allowed root may name a file outside it.
		// The directory and package checks keep the spelled path, like
		// `go run`: a link still counts as living where it is listed.
		resolved, err := resolveSymlinkNearest(abs)
		if err != nil {
			return nil, err
		}
		if err := r.cfg.CheckPath(resolved); err != nil {
			return nil, err
		}
		st, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("entry %q not found: %w", file, err)
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("entry %q is not a .go file", file)
		}
		if seen[resolved] {
			return nil, fmt.Errorf("duplicate file in list: %s", file)
		}
		seen[resolved] = true
		f, err := parser.ParseFile(token.NewFileSet(), abs, nil, parser.PackageClauseOnly)
		if err != nil {
			return nil, fmt.Errorf("reading package clause: %w", err)
		}
		switch d := filepath.Dir(abs); {
		case dir == "":
			dir = d
		case d != dir:
			return nil, fmt.Errorf("named files must all be in one directory; have %s and %s", dir, d)
		}
		switch {
		case name == "":
			name = f.Name.Name
		case f.Name.Name != name:
			return nil, fmt.Errorf("found packages %s (%s) and %s (%s)", name, filepath.Base(files[0]), f.Name.Name, filepath.Base(file))
		}
		gofiles = append(gofiles, abs)
	}
	if len(gofiles) == 1 {
		return r.locateFile(gofiles[0])
	}
	// the file list is part of the synthetic path: two lists over one
	// directory are different programs and must not share the build
	// cache entry.
	names := make([]string, len(gofiles))
	for i, g := range gofiles {
		names[i] = filepath.Base(g)
	}
	sort.Strings(names)
	return &PackageMeta{
		ImportPath: FileImportPrefix + filepath.ToSlash(dir) + "#" + strings.Join(names, ","),
		Name:       name,
		Dir:        dir,
		GoFiles:    gofiles,
		ModulePath: r.loc.ModulePath(),
		Lang:       ModuleLang(dir),
	}, nil
}

// FileImportPrefix starts the synthetic import path of a package built
// from a single .go file by [GoScanResolver.LocateDir]; the absolute file
// path follows it.
const FileImportPrefix = "<file>"
