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
