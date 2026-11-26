package hostbridge

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/podhmo/minigo/runtime"
)

// callScopeStack is the per-goroutine stack of script containers the
// calls running on that goroutine projected to host values. A map or
// slice crossing an `any` parameter becomes a real host map/slice —
// reflect ops like template's {{range}} keep working — while this
// registry lets a callback arg arriving as that projection resolve
// back to the script value, keeping its typedef and method set (a
// FuncMap func's `interface{ Encode() string }` param). Entries are
// pushed per host call and dropped when it returns, so a freed host
// map's address can never resolve to a stale script value.
//
// Each entry also snapshots both sides at the last merge, so pull
// (host->script before a callback) and sync (script->host after it)
// apply only the side that moved: a closure writing the script map
// between callbacks survives the next pull, a host write to the
// projection survives the next sync.
type sliceReg struct {
	base     uintptr
	elemSize uintptr
	n        int
	v        *runtime.Slice
}

// projEntry is one projection's merge state: the script value, the
// host value it crossed as, the script key set/value snapshot and the
// host key set at the last merge. The snapshots tell a script-side
// write (key added, value changed, key deleted) from a host-side one —
// without them every pull or sync would clobber the other side.
type projEntry struct {
	sv   runtime.Value
	rv   reflect.Value
	snap map[string]runtime.Value // script key repr -> value at last merge
	host map[string]bool          // host key repr -> seen at last merge
}

type activeProjection struct {
	recv runtime.Value
	out  any
}

type callScopeStack struct {
	mu sync.Mutex
	// walk folds repeat projections of the same script object inside one
	// merge pass, so write-back reprojection reuses the projection the host
	// already holds instead of minting a fresh one. Ordinary nested host
	// calls use fresh walks: a script mutation made immediately before the
	// inner call must marshal as its current value, not a cached copy.
	walk *runtime.Walk
	// scopes maps a projected host container's pointer to its merge
	// state; slices mirrors the slice registrations so a windowed
	// sub-slice projection resolves by range; used tracks the
	// projections a callback actually consumed so write-back leaves
	// their script value authoritative.
	scopes []map[uintptr]*projEntry
	// scriptScopes mirrors scopes with the reverse lookup: merging a parent
	// container often needs the child projection by script identity, and a
	// row list would make a scan over every entry quadratic.
	scriptScopes []map[runtime.Value]*projEntry
	// walks mirrors scopes: one conversion-identity walk per host call, so
	// two args in the same call share a projection without leaking that
	// copy into a nested or later host call.
	walks  []*runtime.Walk
	slices []sliceReg
	used   map[uintptr]bool
	active map[structProjectionKey]activeProjection
	// originals strongly owns the script values backing struct projections
	// for the duration of this goroutine's outermost host call. The
	// process-wide StructOrig registry is weak; this bounded lifetime is
	// what makes a projection handed back during its owning call round-trip.
	originals map[runtime.Value]bool
	// merging is nonzero while a sync/pull merge pass iterates the
	// scopes. Re-projections a merge drives must not feed back into
	// the merge machinery: a projection registered mid-merge only
	// inflates every later pass, and an eager method call's own host
	// calls re-enter pull/sync — together compounding without bound
	// (issue #1399). While merging, registerProjected skips new
	// entries, nested pullUsedEntries/syncRegistered calls no-op, and
	// struct projections bind callable members lazily.
	merging int
}

// callScopes maps goroutine id -> its stack of projection scopes. The
// goroutine that runs a host call is the only one that registers and
// resolves projections for it; a foreign goroutine (a parked call's
// helper, a template calling from another proc) simply misses the
// lookup and falls back to the plain unbox.
var callScopes sync.Map // int64 -> *callScopeStack

func (m *Marshal) callGid() int64 {
	if m.GoroutineID == nil {
		return 0
	}
	return m.GoroutineID()
}

// scopedWalk starts a marshal over the current conversion-identity walk.
// Ordinary host calls use the walk of their own scope: arguments in one
// call share projections, while nested and later calls cannot reuse a
// stale completed copy. Merge passes use their pass-wide walk instead.
// In-progress struct projections are seeded into either walk so a method
// that re-enters template rendering with its own receiver sees the object
// already being built rather than recursively reprojecting it.
func (m *Marshal) scopedWalk() *HostMarshal {
	st, ok := m.ownStack()
	if !ok {
		return m.NewWalk()
	}
	s := st
	s.mu.Lock()
	var w *runtime.Walk
	if s.merging > 0 {
		if s.walk == nil {
			s.walk = runtime.NewWalk()
		}
		w = s.walk
	} else if n := len(s.walks); n > 0 {
		w = s.walks[n-1]
	}
	for key, ap := range s.active {
		w.Register(key, ap.out)
		w.Register(ap.recv, ap.out)
	}
	s.mu.Unlock()
	if w == nil {
		return m.NewWalk()
	}
	return &HostMarshal{M: m, Walk: w, Budget: HostEvalBudget}
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
	s.active[key] = activeProjection{recv: recv, out: out}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.active, key)
		s.mu.Unlock()
	}
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

// pushCallScope opens a projection scope for the duration of a host
// call; the returned func closes it. Nested calls on one goroutine
// nest scopes so an inner call's close keeps the outer projections.
func (m *Marshal) pushCallScope() func() {
	gid := m.callGid()
	if gid == 0 {
		return func() {}
	}
	st, _ := callScopes.LoadOrStore(gid, &callScopeStack{})
	s := st.(*callScopeStack)
	s.mu.Lock()
	s.scopes = append(s.scopes, map[uintptr]*projEntry{})
	s.scriptScopes = append(s.scriptScopes, map[runtime.Value]*projEntry{})
	s.walks = append(s.walks, runtime.NewWalk())
	if s.used == nil {
		s.used = map[uintptr]bool{}
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.scopes = s.scopes[:len(s.scopes)-1]
		s.scriptScopes = s.scriptScopes[:len(s.scriptScopes)-1]
		s.walks = s.walks[:len(s.walks)-1]
		n := len(s.scopes)
		s.mu.Unlock()
		if n == 0 {
			callScopes.CompareAndDelete(gid, st)
		}
	}
}

// projKey renders a script map key for the merge snapshots. Any
// deterministic form works — the repr compares a key only to its own
// earlier self.
func projKey(k runtime.Value) string {
	return fmt.Sprintf("%#v", runtime.Unwrap(k))
}

// hostKey is projKey for the host side of a projection.
func hostKey(rv reflect.Value) string {
	return fmt.Sprintf("%v", rv)
}

// scriptSnap records a script map's key->value set.
func scriptSnap(s *runtime.Map) map[string]runtime.Value {
	out := make(map[string]runtime.Value, s.Len())
	for i := 0; i < s.Len(); i++ {
		k, v := s.At(i)
		out[projKey(k)] = v
	}
	return out
}

// hostSnap records a host map's key set.
func hostSnap(rv reflect.Value) map[string]bool {
	out := make(map[string]bool, rv.Len())
	it := rv.MapRange()
	for it.Next() {
		out[hostKey(it.Key())] = true
	}
	return out
}

// sameSlot reports whether the script value at a key reads unchanged
// since the snapshot — a scalar whose payload is equal — versus a
// write that replaced it. Containers always report false: their
// writes land inside, which the per-child merge handles.
func sameSlot(cur, old runtime.Value) bool {
	pa, pb := scalarPayload(cur), scalarPayload(old)
	if pa == nil || pb == nil {
		return false
	}
	switch runtime.Unwrap(pa).(type) {
	case *runtime.Map, *runtime.Slice, *runtime.Struct, *runtime.Cell:
		// writes land inside the pointee — the per-child merge
		// decides whether the entry still matches its projection.
		return false
	}
	return pa == pb
}

// ownStack returns the calling goroutine's projection stack; a foreign
// goroutine (a parked call's helper, a template on another proc) has
// none and misses the lookup.
func (m *Marshal) ownStack() (*callScopeStack, bool) {
	gid := m.callGid()
	if gid == 0 {
		return nil, false
	}
	v, ok := callScopes.Load(gid)
	if !ok {
		return nil, false
	}
	return v.(*callScopeStack), true
}

// stackFlag evaluates f under the caller's stack lock — false when the
// goroutine owns no call-scope stack.
func (m *Marshal) stackFlag(f func(*callScopeStack) bool) bool {
	st, ok := m.ownStack()
	if !ok {
		return false
	}
	s := st
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(s)
}

// insideMerge reports whether the calling goroutine is running a
// sync/pull merge pass — projections a merge builds ride on it (issue
// #1399): re-projecting eagerly calls the same members the outer
// projection did, and each eager call's own host calls re-enter the
// merge. Callable members bind lazily instead.
func (m *Marshal) insideMerge() bool {
	return m.stackFlag(func(s *callScopeStack) bool { return s.merging > 0 })
}
