package resolve

import (
	"path/filepath"
	"strings"
)

// ModulePath returns the anchoring module's path, or "" when there is no
// go.mod.
func (r *GoScanResolver) ModulePath() string { return r.loc.ModulePath() }

// RootDir returns the directory of the anchoring go.mod, or startDir when
// there is none.
func (r *GoScanResolver) RootDir() string { return r.loc.RootDir() }

// markStandard corrects PackageMeta.Standard for a custom GOROOT: a
// scratch tree outside the host toolchain is still standard-library source.
func (r *GoScanResolver) markStandard(meta *PackageMeta, err error) (*PackageMeta, error) {
	if err != nil || meta == nil || r.goRoot == "" {
		return meta, err
	}
	root, rerr := filepath.Abs(r.goRoot)
	dir, derr := filepath.Abs(meta.Dir)
	if rerr == nil && derr == nil {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			meta.Standard = true
		}
	}
	return meta, nil
}

