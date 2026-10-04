package resolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/podhmo/minigo/pkg/locator"
)

// GoScanResolver adapts go-scan's locator: it walks go.work / go.mod /
// replace / GOROOT / GOMODCACHE without ever invoking `go list`.
type GoScanResolver struct {
	loc *locator.Locator
	cfg BuildConfig
}

// NewGoScanResolver creates a resolver rooted at the module containing
// startDir. Module resolution (GOROOT + module cache) is enabled.
func NewGoScanResolver(startDir string, cfg BuildConfig) (*GoScanResolver, error) {
	l, err := locator.New(startDir, locator.WithGoModuleResolver())
	if err != nil {
		return nil, fmt.Errorf("creating locator at %s: %w", startDir, err)
	}
	return &GoScanResolver{loc: l, cfg: cfg}, nil
}

// Locate implements Resolver.
func (r *GoScanResolver) Locate(ctx context.Context, fromDir, importPath string) (*PackageMeta, error) {
	dir, err := r.loc.FindPackageDirFrom(fromDir, importPath)
	if err != nil {
		return nil, fmt.Errorf("resolving import %q: %w", importPath, err)
	}
	if err := r.cfg.CheckDir(dir); err != nil {
		return nil, err
	}
	return ReadPackageFiles(dir, importPath, r.cfg)
}

// LocateDir implements Resolver. The package's import path is derived
// best-effort via the locator; directories outside any module get a
// synthetic path so they still have an identity.
func (r *GoScanResolver) LocateDir(ctx context.Context, dir string) (*PackageMeta, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := r.cfg.CheckDir(abs); err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("entry directory %q not found: %w", dir, err)
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
