package hostbridge

import "github.com/podhmo/minigo/runtime"

type activeProjection struct {
	recv runtime.Value
	out  any
}

// beginActiveProjection marks a struct projection as under construction
// until the returned func runs. Only active projections cross conversion
// walks; completed ones would be stale copies.
func (m *Marshal) beginActiveProjection(key structProjectionKey, recv runtime.Value, out any, walk *runtime.Walk) func() {
	st, ok := m.ownStack()
	if !ok {
		return func() {}
	}
	s := st
	s.mu.Lock()
	if s.active == nil {
		s.active = map[structProjectionKey]activeProjection{}
	}
	s.active[key] = activeProjection{recv: recv, out: out}
	walk.SetMeta(key, s.scriptEpoch)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.active, key)
		s.mu.Unlock()
	}
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
