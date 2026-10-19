package resolve

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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

// NewGoScanResolver creates a resolver for the module whose go.mod is
// nearest at or above startDir. Without such a go.mod it anchors at
// startDir and still resolves GOROOT and module-cache packages.
func NewGoScanResolver(startDir string, cfg BuildConfig) (*GoScanResolver, error) {
	l, err := locator.New(startDir, locator.WithGoModuleResolver())
	if err != nil {
		return nil, fmt.Errorf("creating locator at %s: %w", startDir, err)
	}
	return &GoScanResolver{loc: l, cfg: cfg}, nil
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

// FileImportPrefix starts the synthetic import path of a package built
// from a single .go file by [GoScanResolver.LocateDir]; the absolute file
// path follows it.
const FileImportPrefix = "<file>"
