package minigo

import (
	stdlibhost "github.com/podhmo/minigo/internal/stdlib/host"
	stdlibimportmanager "github.com/podhmo/minigo/internal/stdlib/importmanager"
	stdinspect "github.com/podhmo/minigo/internal/stdlib/inspect"
	"github.com/podhmo/minigo/runtime"
)

// nestedNewEngine inherits the outer host policy even when script-supplied
// options try to replace it. AllowedRoots are handled by binding empty
// packages below: Engine.WithResolver would otherwise replace the confined
// resolver.
func (e *Engine) nestedNewEngine(startDir string, opts ...Option) *Engine {
	if e.hostPolicy != nil {
		policy := e.hostPolicy
		opts = append(append([]Option(nil), opts...), Option(func(ne *Engine) { ne.hostPolicy = policy }))
	}
	return NewEngine(startDir, opts...)
}

// registerMinigoBindings registers the embedding-facing packages whose
// implementation lives in this repository. They are host-bound for the
// same reason as the stdlib intrinsics, but unlike stdlib packages they
// are part of minigo's own programmable API.
func (e *Engine) registerMinigoBindings(h *hostHelpers) {
	e.pending.Register("minigo.dev/host", func() { stdlibhost.Install(e.bindBound, h, h.env) })
	e.pending.Register("github.com/podhmo/minigo/host", func() { stdlibhost.Install(e.bindBound, h, h.env) })
	e.pending.Register("minigo.dev/importmanager", func() { stdlibimportmanager.Install(e.bindBound, h) })
	e.pending.Register("github.com/podhmo/minigo/pkg/importmanager", func() { stdlibimportmanager.Install(e.bindBound, h) })
	// A nested engine can replace its resolver, so exposing the constructor
	// under AllowedRoots would escape the outer confinement. As with
	// os/exec, restricted engines see a bound but empty package.
	e.pending.Register("github.com/podhmo/minigo", func() {
		symbols := map[string]runtime.Value{}
		if len(e.cfg.AllowedRoots) == 0 {
			symbols = minigoSelfSymbols(e.nestedNewEngine)
		}
		e.bindBound("github.com/podhmo/minigo", symbols)
	})
	e.pending.Register("github.com/podhmo/minigo/resolve", func() {
		symbols := map[string]runtime.Value{}
		if len(e.cfg.AllowedRoots) == 0 {
			symbols = resolveSelfSymbols()
		}
		e.bindBound("github.com/podhmo/minigo/resolve", symbols)
	})
	stdinspectHooks := stdinspect.Hooks{
		LoadPath:        e.loadPath,
		LoadPathLenient: e.loadPathLenient,
		LoadDirLenient:  e.loadDirLenient,
		LoadFileLenient: e.loadFileLenient,
		FSPath:          e.fsPath,
		Materialize:     e.materialize,
		BoundOf:         e.BoundOf,
		SourceOf:        e.SourceOf,
	}
	e.pending.Register("minigo.dev/inspect", func() { stdinspect.Install(e.bindBound, stdinspectHooks) })
	e.pending.Register("github.com/podhmo/minigo/inspect", func() { stdinspect.Install(e.bindBound, stdinspectHooks) })
}
