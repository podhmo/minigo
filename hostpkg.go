package minigo

import "github.com/podhmo/minigo/runtime"

// hostPackageOf is the typedef lookup's install entry: for a
// registered (bound) path it guarantees the package's install before
// returning it, while an unregistered path only answers the packages
// already present. P2-12 runs the per-package Install inside
// installBound; today install is still eager, so e.pkgs already holds
// every bound package.
func (e *Engine) hostPackageOf(path string) *runtime.Package {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.names.Bound(path) {
		e.installBound(path)
	}
	return e.pkgs[path]
}

// installBound installs a registered path's bound package on first
// use. Install is still eager (installStdlib at engine/session
// construction), so the package is already in e.pkgs; P2-12 swaps this
// for the per-package lazy internal/stdlib/<path>.Install call.
func (e *Engine) installBound(path string) {
}
