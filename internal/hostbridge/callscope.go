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

type callScopeStack struct {
	mu sync.Mutex
	// scopes maps a projected host container's pointer to its merge
	// state; slices mirrors the slice registrations so a windowed
	// sub-slice projection resolves by range; used tracks the
	// projections a callback actually consumed so write-back leaves
	// their script value authoritative.
	scopes []map[uintptr]*projEntry
	slices []sliceReg
	used   map[uintptr]bool
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
	if s.used == nil {
		s.used = map[uintptr]bool{}
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.scopes = s.scopes[:len(s.scopes)-1]
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

// registerProjected records v for the duration of the running host
// call: v is a script map or slice that crossed an `any` parameter as
// the plain host container out — the projection keeps no method set,
// so a callback receiving out back (a FuncMap arg) resolves to v,
// which still carries the typedef's methods. Anonymous containers
// register too: the shared-identity model applies to every map/slice.
func (m *Marshal) registerProjected(v runtime.Value, out any) {
	switch v.(type) {
	case *runtime.Map, *runtime.Slice:
	default:
		return
	}
	rv := reflect.ValueOf(out)
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() || (rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice) || rv.IsNil() {
		return
	}
	var reg *sliceReg
	if ss, ok := v.(*runtime.Slice); ok && rv.Kind() == reflect.Slice {
		reg = &sliceReg{base: rv.Pointer(), elemSize: rv.Type().Elem().Size(), n: len(ss.Elems), v: ss}
	}
	if rv.Pointer() == 0 {
		return
	}
	st, ok := m.ownStack()
	if !ok {
		return
	}
	e := &projEntry{sv: v, rv: rv}
	if sm, ok := v.(*runtime.Map); ok {
		e.snap = scriptSnap(sm)
		e.host = hostSnap(rv)
	}
	s := st
	s.mu.Lock()
	if n := len(s.scopes); n > 0 {
		s.scopes[n-1][rv.Pointer()] = e
		if reg != nil {
			s.slices = append(s.slices, *reg)
		}
	}
	s.mu.Unlock()
}

// projectedVal returns the merge state registered for the host
// container an `any` parameter is receiving — the projection an outer
// call on this goroutine handed in — else nil.
func (m *Marshal) projectedVal(rv reflect.Value) *projEntry {
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	var p uintptr
	if !rv.IsValid() || (rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice) || rv.IsNil() {
		return nil
	}
	p = rv.Pointer()
	st, ok := m.ownStack()
	if !ok {
		return nil
	}
	s := st
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.scopes) - 1; i >= 0; i-- {
		if e, ok := s.scopes[i][p]; ok {
			// a sub-slice projection shares its parent's leading
			// address — `{{f (slice . 0 1)}}` — but the script value it
			// stands for must have the projection's length, else the
			// callback would see the range it was not given.
			if ss, ok := e.sv.(*runtime.Slice); ok && rv.Len() != len(ss.Elems) {
				continue
			}
			s.used[p] = true
			return e
		}
	}
	if rv.Kind() != reflect.Slice {
		return nil
	}
	// a windowed sub-slice projection resolves by range: its address is
	// base + idx*elemSize of a registered parent's backing. The view
	// shares the parent's Elems, so element writes reach it.
	for _, r := range s.slices {
		if r.elemSize == 0 || p < r.base || (p-r.base)%r.elemSize != 0 {
			continue
		}
		idx := int((p - r.base) / r.elemSize)
		if idx < 0 || idx+rv.Len() > r.n {
			continue
		}
		s.used[r.base] = true
		return &projEntry{sv: &runtime.Slice{Elems: r.v.Elems[idx : idx+rv.Len()], Typ: r.v.Typ}, rv: rv}
	}
	return nil
}
