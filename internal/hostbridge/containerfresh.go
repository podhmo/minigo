package hostbridge

import "github.com/podhmo/minigo/runtime"

// projectedContainerStale reports whether a registered container projection
// predates the latest script mutation barrier.
func (m *Marshal) projectedContainerStale(e *projEntry) bool {
	return m.stackFlag(func(s *callScopeStack) bool { return e.epoch < s.scriptEpoch })
}

// markProjectedContainerFresh records a container projection as synchronized
// at the current script epoch.
func (m *Marshal) markProjectedContainerFresh(e *projEntry) {
	st, ok := m.ownStack()
	if !ok {
		return
	}
	st.mu.Lock()
	e.epoch = st.scriptEpoch
	st.mu.Unlock()
}

// refreshProjectedContainer synchronizes a reused container projection after
// a mutation barrier. Unlike a struct projection the host container is the
// object template already holds, so updating it in place preserves identity
// while making method-side writes visible.
func (m *Marshal) refreshProjectedContainer(v runtime.Value, out any, vc runtime.VMCaller) {
	e := m.projectedForScript(v)
	if e == nil || !m.projectedContainerStale(e) {
		return
	}
	m.syncProjected(e, vc)
	m.markProjectedContainerFresh(e)
}
