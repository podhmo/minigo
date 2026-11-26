package hostbridge

import "github.com/podhmo/minigo/runtime"

type activeProjection struct {
	recv runtime.Value
	out  any
}

// beginActiveProjection marks a struct projection as under construction
// until the returned func runs. Only active projections cross conversion
// walks; completed ones would be stale copies.
func (m *Marshal) beginActiveProjection(key structProjectionKey, recv runtime.Value, out any) func() {
	st, ok := m.ownStack()
	if !ok {
		return func() {}
	}
	s := st
	s.mu.Lock()
	if s.active == nil {
		s.active = map[structProjectionKey]activeProjection{}
	}
	if s.projectionEpochs == nil {
		s.projectionEpochs = map[structProjectionKey]uint64{}
	}
	s.active[key] = activeProjection{recv: recv, out: out}
	s.projectionEpochs[key] = s.scriptEpoch
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.active, key)
		s.mu.Unlock()
	}
}

// noteScriptCallback marks a mutation barrier: a script callback may have
// written through captured state even when no projected container was an
// argument. Struct projections created before the barrier refresh when
// conversion identity next sees them.
func (m *Marshal) noteScriptCallback() {
	st, ok := m.ownStack()
	if !ok {
		return
	}
	st.mu.Lock()
	st.scriptEpoch++
	st.mu.Unlock()
}

// projectionStale reports whether key's projection predates the latest
// script-callback mutation barrier on this goroutine.
func (m *Marshal) projectionStale(key structProjectionKey) bool {
	return m.stackFlag(func(s *callScopeStack) bool {
		return s.projectionEpochs[key] < s.scriptEpoch
	})
}

// markProjectionFresh records that key's projection was refreshed at the
// current script epoch.
func (m *Marshal) markProjectionFresh(key structProjectionKey) {
	st, ok := m.ownStack()
	if !ok {
		return
	}
	st.mu.Lock()
	if s := st; s.projectionEpochs != nil {
		s.projectionEpochs[key] = s.scriptEpoch
	}
	st.mu.Unlock()
}

// activeProjectionHit reports whether key names a projection still being
// built on this goroutine. It must be returned as is even when stale:
// refreshing it would invoke the same method recursively.
func (m *Marshal) activeProjectionHit(key structProjectionKey) bool {
	return m.stackFlag(func(s *callScopeStack) bool {
		_, ok := s.active[key]
		return ok
	})
}

// retainProjectionOrig keeps a projected struct's script original alive
// for the owning host call. Unlike the projection-identity walk, this is
// not a conversion cache, so retaining it cannot serve stale values.
func (m *Marshal) retainProjectionOrig(v runtime.Value) {
	st, ok := m.ownStack()
	if !ok {
		return
	}
	s := st
	s.mu.Lock()
	if s.originals == nil {
		s.originals = map[runtime.Value]bool{}
	}
	s.originals[v] = true
	s.mu.Unlock()
}
