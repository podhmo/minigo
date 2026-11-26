package hostbridge

import "github.com/podhmo/minigo/runtime"

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
func (m *Marshal) projectionStale(walk *runtime.Walk, key structProjectionKey) bool {
	fresh, ok := walk.Meta(key)
	if !ok {
		return true
	}
	return fresh.(uint64) < m.currentScriptEpoch()
}

// markProjectionFresh records that key's projection was refreshed at the
// current script epoch.
func (m *Marshal) markProjectionFresh(walk *runtime.Walk, key structProjectionKey) {
	walk.SetMeta(key, m.currentScriptEpoch())
}

func (m *Marshal) currentScriptEpoch() uint64 {
	st, ok := m.ownStack()
	if !ok {
		return 0
	}
	st.mu.Lock()
	epoch := st.scriptEpoch
	st.mu.Unlock()
	return epoch
}
