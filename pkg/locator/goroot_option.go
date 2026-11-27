package locator

import "path/filepath"

// WithGOROOT uses goRoot instead of the toolchain's `go env GOROOT`
// answer. A scratch or symlink-farm GOROOT lets an in-process caller run
// against patched standard-library sources without changing the process
// environment. The module cache lookup is independent and unchanged.
func WithGOROOT(goRoot string) Option {
	if abs, err := filepath.Abs(goRoot); err == nil {
		goRoot = abs
	}
	return func(l *Locator) {
		l.goRoot = goRoot
	}
}
