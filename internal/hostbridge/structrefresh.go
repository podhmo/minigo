package hostbridge

import "github.com/podhmo/minigo/runtime"

// noteImpureMethodCallback marks the mutation barrier for a method call
// that may write. Expression-only methods cannot mutate state, so their
// refresh does not make the projection stale again.
func (m *Marshal) noteImpureMethodCallback(f *runtime.Function) {
	if !pureMethod(f) {
		m.noteScriptCallback()
	}
}
