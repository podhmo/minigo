package hostbridge

import (
	"reflect"
	"sync"

	"github.com/podhmo/minigo/runtime"
)

// callScopeStack is the per-goroutine stack of script containers the
// calls running on that goroutine projected to host values. A
// named-typedef map or slice crosses an `any` parameter as a real
// host map/slice — reflect ops like template's {{range}} keep
// working — while this registry lets a callback arg arriving as that
// projection resolve back to the script value, keeping its typedef and
// method set (a FuncMap func's `interface{ Encode() string }` param).
// Entries are pushed per host call and dropped when it returns, so a
// freed host map's address can never resolve to a stale script value.
// sliceReg is a slice projection's identity: its leading address and
// element stride let a window of it ({{f (slice . a b)}}) resolve to the
// same window of the script slice.
type sliceReg struct {
	base     uintptr
	elemSize uintptr
	n        int
	v        *runtime.Slice
}

type callScopeStack struct {
	mu sync.Mutex
	// scopes maps a projected host container's pointer to the script
	// value it was made from; slices mirrors the slice registrations so
	// a windowed sub-slice projection resolves by range; used tracks
	// the projections a callback actually consumed so write-back
	// leaves their script value alone.
	scopes []map[uintptr]runtime.Value
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
	s.scopes = append(s.scopes, map[uintptr]runtime.Value{})
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

// registerProjected records v for the duration of the running host
// call: v is a script map or slice of a NAMED typedef that crossed an
// `any` parameter as the plain host container out — the projection
// keeps no method set, so a callback receiving out back (a FuncMap
// arg) resolves to v, which still carries the typedef's methods.
func (m *Marshal) registerProjected(v runtime.Value, out any) {
	var td *runtime.TypeDef
	switch x := v.(type) {
	case *runtime.Map:
		td = x.Typ
	case *runtime.Slice:
		td = x.Typ
	default:
		return
	}
	if td == nil || td.Name == "" {
		return
	}
	rv := reflect.ValueOf(out)
	if !rv.IsValid() || (rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice) || rv.IsNil() {
		return
	}
	var reg *sliceReg
	if ss, ok := v.(*runtime.Slice); ok && rv.Kind() == reflect.Slice {
		reg = &sliceReg{base: rv.Pointer(), elemSize: rv.Type().Elem().Size(), n: len(ss.Elems), v: ss}
	}
	gid := m.callGid()
	if gid == 0 {
		return
	}
	st, ok := callScopes.Load(gid)
	if !ok {
		return
	}
	if rv.Pointer() == 0 {
		return
	}
	s := st.(*callScopeStack)
	s.mu.Lock()
	if n := len(s.scopes); n > 0 {
		s.scopes[n-1][rv.Pointer()] = v
		if reg != nil {
			s.slices = append(s.slices, *reg)
		}
	}
	s.mu.Unlock()
}

// projectedVal returns the script value registered for the host
// container an `any` parameter is receiving — the projection an outer
// call on this goroutine handed in — else nil.
func (m *Marshal) projectedVal(rv reflect.Value) runtime.Value {
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	var p uintptr
	if !rv.IsValid() || (rv.Kind() != reflect.Map && rv.Kind() != reflect.Slice) || rv.IsNil() {
		return nil
	}
	p = rv.Pointer()
	gid := m.callGid()
	if gid == 0 {
		return nil
	}
	st, ok := callScopes.Load(gid)
	if !ok {
		return nil
	}
	s := st.(*callScopeStack)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.scopes) - 1; i >= 0; i-- {
		if v, ok := s.scopes[i][p]; ok {
			// a sub-slice projection shares its parent's leading
			// address — `{{f (slice . 0 1)}}` — but the script value it
			// stands for must have the projection's length, else the
			// callback would see the range it was not given.
			if ss, ok := v.(*runtime.Slice); ok && rv.Len() != len(ss.Elems) {
				continue
			}
			s.used[p] = true
			return v
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
		return &runtime.Slice{Elems: r.v.Elems[idx : idx+rv.Len()], Typ: r.v.Typ}
	}
	return nil
}

// pullProjected refreshes the script container from the host
// projection just before a callback runs — host-side writes made since
// the last callback (a template's {{set}}, a helper writing the
// projection) merge in so the callback reads shared state like gc's
// would. Containers inside keep their script identity: a nested map
// recurses rather than being replaced, and its host child registers so
// a later callback receiving it resolves back.
func (m *Marshal) pullProjected(v runtime.Value, rv reflect.Value, vc runtime.VMCaller) {
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return
	}
	switch s := v.(type) {
	case *runtime.Map:
		if rv.Kind() != reflect.Map || rv.IsNil() {
			return
		}
		krt := rv.Type().Key()
		disps, _ := s.SnapshotKeys()
		for _, dk := range disps {
			hk, err := m.ToReflect(dk, krt, vc)
			if err != nil || !rv.MapIndex(hk).IsValid() {
				s.Delete(dk)
			}
		}
		it := rv.MapRange()
		for it.Next() {
			hk, ev := it.Key(), it.Value()
			sk := GoSlotValueOf(hk)
			cur, has := s.Get(sk)
			m.pullElem(cur, has, sk, s, ev, vc)
		}
	case *runtime.Slice:
		if rv.Kind() != reflect.Slice {
			return
		}
		for i := 0; i < rv.Len() && i < len(s.Elems); i++ {
			m.pullElem(s.Elems[i], true, nil, s, rv.Index(i), vc, i)
		}
	}
}

// pullElem folds one host element/entry into the script container: a
// nested container recurses (keeping the script value's identity and
// registering the new host child), an unchanged scalar keeps its tag,
// anything else is replaced.
func (m *Marshal) pullElem(cur runtime.Value, has bool, sk runtime.Value, parent runtime.Value, ev reflect.Value, vc runtime.VMCaller, idxs ...int) {
	if ev.Kind() == reflect.Interface && !ev.IsNil() {
		ev = ev.Elem()
	}
	if ev.Kind() == reflect.Map || ev.Kind() == reflect.Slice {
		var inner runtime.Value
		if u := runtime.Unwrap(cur); u != nil {
			switch u.(type) {
			case *runtime.Map, *runtime.Slice:
				inner = u
			}
		}
		if inner != nil {
			m.pullProjected(inner, ev, vc)
			m.registerProjected(inner, ev.Interface())
			return
		}
	}
	if has && unchangedScalar(cur, ev) {
		return
	}
	nv := GoSlotValueOf(ev)
	switch p := parent.(type) {
	case *runtime.Map:
		if has {
			p.Insert(sk, retagElem(cur, nv))
		} else {
			p.Insert(sk, nv)
		}
	case *runtime.Slice:
		p.Elems[idxs[0]] = retagElem(cur, nv)
	}
}

// syncProjected writes a callback-held script container's contents
// back into the host projection the host keeps reading after the call
// — template {{range}} sees the callback's writes like gc's shared map
// would. Maps gain and lose keys; a slice resyncs elements it shares
// length with (a host slice header can't grow).
func (m *Marshal) syncProjected(v runtime.Value, rv reflect.Value, vc runtime.VMCaller) {
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return
	}
	switch s := v.(type) {
	case *runtime.Map:
		if rv.Kind() != reflect.Map || rv.IsNil() {
			return
		}
		krt, ert := rv.Type().Key(), rv.Type().Elem()
		for _, mk := range rv.MapKeys() {
			if _, ok := s.Get(GoSlotValueOf(mk)); !ok {
				rv.SetMapIndex(mk, reflect.Value{})
			}
		}
		for i := 0; i < s.Len(); i++ {
			sk, sv := s.At(i)
			hk, err := m.ToReflect(sk, krt, vc)
			if err != nil {
				continue
			}
			hv, err := m.ToReflect(sv, ert, vc)
			if err != nil {
				continue
			}
			rv.SetMapIndex(hk, hv)
			// a child container just re-projected to a new host
			// value — register it so a later callback receiving the
			// child resolves back to the script value.
			m.registerProjected(sv, hv.Interface())
		}
	case *runtime.Slice:
		if rv.Kind() != reflect.Slice {
			return
		}
		ert := rv.Type().Elem()
		for i := 0; i < rv.Len() && i < len(s.Elems); i++ {
			ev, err := m.ToReflect(s.Elems[i], ert, vc)
			if err != nil {
				continue
			}
			if el := rv.Index(i); el.CanSet() {
				el.Set(ev)
				m.registerProjected(s.Elems[i], el.Interface())
			}
		}
	}
}

// projectedUsed reports whether a callback on this goroutine consumed
// the projection with this pointer — its script value is authoritative
// now, so write-back must not restore the host copy's stale contents.
func (m *Marshal) projectedUsed(p uintptr) bool {
	gid := m.callGid()
	if gid == 0 {
		return false
	}
	st, ok := callScopes.Load(gid)
	if !ok {
		return false
	}
	s := st.(*callScopeStack)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[p]
}
