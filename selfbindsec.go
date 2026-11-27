package minigo

// nestedNewEngine resolves only startDir in the parent's virtual cwd; later
// Run/Package dir refs keep host-resolver semantics. It inherits the parent's
// security posture even when script options try to replace it. AllowedRoots
// are handled at registration: Engine.WithResolver would otherwise replace the confined resolver.
func (e *Engine) nestedNewEngine(startDir string, opts ...Option) *Engine {
	if p, err := e.fsPath(startDir); err == nil {
		startDir = p
	}
	opts = append([]Option(nil), opts...)
	if e.hostPolicy != nil {
		parent := e.hostPolicy
		opts = append(opts, Option(func(ne *Engine) {
			child := ne.hostPolicy
			if child == nil {
				ne.hostPolicy = parent
			} else {
				ne.hostPolicy = func(path, symbol string) bool { return parent(path, symbol) && child(path, symbol) }
			}
		}))
	}
	denied := deniedPackageModes(e.pkgModes)
	if len(denied) > 0 {
		opts = append(opts, Option(func(ne *Engine) {
			if ne.pkgModes == nil {
				ne.pkgModes = map[string]PackageMode{}
			}
			for path, mode := range denied {
				ne.pkgModes[path] = mode
			}
		}))
	}
	return NewEngine(startDir, opts...)
}

func deniedPackageModes(modes map[string]PackageMode) map[string]PackageMode {
	denied := map[string]PackageMode{}
	for path, mode := range modes {
		if mode == ModeDeny {
			denied[path] = mode
		}
	}
	return denied
}

// nestedWithPackageModes composes a script-visible package-mode option
// with the outer engine's denials. Applying it after construction can
// change or narrow other modes, but never re-enables a parent-denied path.
func (e *Engine) nestedWithPackageModes(modes map[string]PackageMode) Option {
	base := WithPackageModes(modes)
	denied := deniedPackageModes(e.pkgModes)
	if len(denied) == 0 {
		return base
	}
	return func(ne *Engine) {
		base(ne)
		if ne.pkgModes == nil {
			ne.pkgModes = map[string]PackageMode{}
		}
		for path, mode := range denied {
			ne.pkgModes[path] = mode
		}
	}
}

// nestedWithHostPolicy composes a script-visible policy option with the
// outer engine's policy. Applying the option after construction can narrow
// the child further, but never re-enable a symbol the parent denied.
func (e *Engine) nestedWithHostPolicy(allow func(importPath, symbol string) bool) Option {
	if e.hostPolicy == nil {
		return WithHostPolicy(allow)
	}
	parent := e.hostPolicy
	return func(ne *Engine) {
		if allow == nil {
			ne.hostPolicy = parent
			return
		}
		ne.hostPolicy = func(path, symbol string) bool { return parent(path, symbol) && allow(path, symbol) }
	}
}
