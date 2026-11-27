package testrun

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/podhmo/minigo/resolve"
)

// variantResolver splices the internal *_test.go files into the entry
// directory's LocateDir result — the test-variant build — while every
// other resolution passes through untouched.
type variantResolver struct {
	inner     *resolve.GoScanResolver
	dir       string // absolute
	testFiles []string
	pkgName   string // clause the internal test files declare
}

func (r *variantResolver) Locate(ctx context.Context, fromDir, importPath string) (*resolve.PackageMeta, error) {
	return r.inner.Locate(ctx, fromDir, importPath)
}

func (r *variantResolver) LocateDir(ctx context.Context, dir string) (*resolve.PackageMeta, error) {
	meta, err := r.inner.LocateDir(ctx, dir)
	if err != nil {
		// Only an empty constraint-filtered file list means test-only;
		// `go test` builds those test files alone.
		if dir == r.dir && len(r.testFiles) > 0 && errors.Is(err, resolve.ErrNoBuildableGoFiles) {
			return r.testOnlyMeta(dir), nil
		}
		return nil, err
	}
	if meta.Dir == r.dir && len(r.testFiles) > 0 {
		cp := *meta
		cp.GoFiles = append(append([]string{}, meta.GoFiles...), r.testFiles...)
		sort.Strings(cp.GoFiles)
		return &cp, nil
	}
	return meta, nil
}

// testOnlyMeta synthesizes a PackageMeta for a directory that has
// *_test.go files but no buildable non-test sources.
func (r *variantResolver) testOnlyMeta(dir string) *resolve.PackageMeta {
	importPath := pathOfDir(dir)
	if mp, root := r.inner.ModulePath(), r.inner.RootDir(); mp != "" && root != "" {
		if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
			importPath = mp + "/" + filepath.ToSlash(rel)
		}
	}
	files := append([]string{}, r.testFiles...)
	sort.Strings(files)
	return &resolve.PackageMeta{
		ImportPath: importPath,
		Name:       r.pkgName,
		Dir:        dir,
		GoFiles:    files,
		ModulePath: r.inner.ModulePath(),
		Lang:       resolve.ModuleLang(dir),
	}
}
