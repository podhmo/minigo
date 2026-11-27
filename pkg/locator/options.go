package locator

// Option is a functional option for configuring the Locator.
type Option func(*Locator)

// WithOverlay provides in-memory file content for go.mod.
func WithOverlay(overlay Overlay) Option {
	return func(l *Locator) {
		if l.overlay == nil {
			l.overlay = make(Overlay)
		}
		for k, v := range overlay {
			l.overlay[k] = v
		}
	}
}

// WithGoModuleResolver enables resolving packages from GOROOT and the module cache.
func WithGoModuleResolver() Option {
	return func(l *Locator) {
		l.UseGoModuleResolver = true
	}
}

// WithGOROOT uses goRoot instead of the toolchain's `go env GOROOT`
// answer. A scratch or symlink-farm GOROOT lets an in-process caller run
// against patched standard-library sources without changing the process
// environment. The module cache lookup is independent and unchanged.
func WithGOROOT(goRoot string) Option {
	return func(l *Locator) {
		l.goRoot = goRoot
	}
}
