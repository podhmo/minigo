package hostbridge

import "github.com/podhmo/minigo/runtime"

// mergeEntries marks the caller's stack as merging and collects the
// entries the pass will merge (usedOnly picks only consumed ones).
// A nested pass — a merge-time re-projection's own host calls re-entering
// sync/pull — returns nil so it cannot rework the same entries and feed
// the projection feedback loop (issue #1399). Callers defer endMerge.
func (m *Marshal) mergeEntries(usedOnly bool) (*callScopeStack, []*projEntry) {
	st, ok := m.ownStack()
	if !ok {
		return nil, nil
	}
	s := st
	s.mu.Lock()
	if s.merging > 0 {
		s.mu.Unlock()
		return nil, nil
	}
	s.merging++
	// Identity reuse lasts exactly one merge pass. Keeping the walk into a
	// later pass would cache containers beside the snapshot machinery and
	// could serve the pre-mutation copy again.
	s.walk = runtime.NewWalk()
	var es []*projEntry
	for i := range s.scopes {
		for p, e := range s.scopes[i] {
			if usedOnly && !s.used[p] {
				continue
			}
			es = append(es, e)
		}
	}
	s.mu.Unlock()
	return s, es
}

// endMerge releases the merge pass mergeEntries started.
func (m *Marshal) endMerge(s *callScopeStack) {
	s.mu.Lock()
	s.merging--
	if s.merging == 0 {
		s.walk = nil
	}
	s.mu.Unlock()
}

// syncRegistered mirrors every registered projection's script state
// into its host counterpart. A FuncMap func's writes reach the map by
// script identity — a closure mutating a captured map touches no arg —
// so after each callback every entry syncs, not just the resolved
// ones.
func (m *Marshal) syncRegistered(vc runtime.VMCaller) {
	m.noteScriptCallback()
	s, es := m.mergeEntries(false)
	if s == nil {
		return
	}
	defer m.endMerge(s)
	for _, e := range es {
		m.syncProjected(e, vc)
	}
}

// pullUsedEntries runs one last host->script merge on every consumed
// projection before the call's write-back finishes — a host write that
// landed after the last callback (or on a child only a callback held,
// never the arg itself) still reaches the script value.
func (m *Marshal) pullUsedEntries(vc runtime.VMCaller) {
	s, es := m.mergeEntries(true)
	if s == nil {
		return
	}
	defer m.endMerge(s)
	seen := map[runtime.Value]bool{}
	for _, e := range es {
		m.pullProjected(e, vc, seen)
	}
}

// projectedUsed reports whether a callback on this goroutine consumed
// the projection with this pointer — its script value is authoritative
// now, so write-back must not restore the host copy's stale contents.
func (m *Marshal) projectedUsed(p uintptr) bool {
	return m.stackFlag(func(s *callScopeStack) bool { return s.used[p] })
}
