package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// pull/sync: two-sided merge of a projected container between the
// script value and the host value it crossed as. Snapshots taken at
// registration and refreshed at every merge tell which side moved, so
// a pull applies only host-side deltas (a template's {{range}} sees
// the freshest host writes, a closure's script writes survive) and a
// sync applies only script-side deltas (the callee's host writes to
// the projection survive).

// pullProjected merges the host projection's changes into the script
// container just before a callback runs — host-side writes made since
// the last merge (a template's {{set}}, a helper writing the
// projection) land so the callback reads shared state like gc's
// would. Script-side writes survive: the snapshots gate deletions to
// keys present at the last merge, and a value the script rewrote
// stays. seen breaks self-referential containers (m["self"] = m).
func (m *Marshal) pullProjected(e *projEntry, vc runtime.VMCaller, seen map[runtime.Value]bool) {
	if seen[e.sv] {
		return
	}
	seen[e.sv] = true
	rv := e.rv
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return
	}
	switch s := e.sv.(type) {
	case *runtime.Map:
		if rv.Kind() != reflect.Map || rv.IsNil() {
			return
		}
		krt := rv.Type().Key()
		disps, _ := s.SnapshotKeys()
		nanScript := 0
		for _, dk := range disps {
			if nanKey(dk) {
				nanScript++
			}
		}
		for _, dk := range disps {
			if _, tracked := e.snap[projKey(dk)]; !tracked {
				continue // script-added since the merge — keep
			}
			hk, err := m.ToReflect(dk, krt, vc)
			if err != nil || !rv.MapIndex(hk).IsValid() {
				s.Delete(dk) // a key the merge knew and the host dropped
			}
		}
		it := rv.MapRange()
		nanSeen := 0
		for it.Next() {
			hk, ev := it.Key(), it.Value()
			sk := GoSlotValueOf(hk)
			if nanReflectKey(hk) {
				// a NaN key never resolves under Get — one existing
				// script NaN entry stands for each host one; extra
				// host NaN keys still insert like gc's repeated
				// inserts would.
				nanSeen++
				if nanSeen <= nanScript {
					continue
				}
			}
			cur, has := s.Get(sk)
			if has {
				if old, ok := e.snap[projKey(sk)]; ok && !sameSlot(cur, old) && !isContainerVal(cur) {
					continue // the script rewrote this key — script wins
				}
			}
			m.pullElem(cur, has, sk, s, ev, vc, seen)
		}
		e.host = hostSnap(rv)
		// snapshot only the keys the host actually mirrors — folding a
		// script-side add into snap would let sync skip its push, and
		// the next pull would then drop it as host-deleted.
		snap := map[string]runtime.Value{}
		ds2, _ := s.SnapshotKeys()
		for _, dk := range ds2 {
			if hk, err := m.ToReflect(dk, krt, vc); err == nil && rv.MapIndex(hk).IsValid() {
				if v, ok := s.Get(dk); ok {
					snap[projKey(dk)] = v
				}
			}
		}
		e.snap = snap
	case *runtime.Slice:
		if rv.Kind() != reflect.Slice {
			return
		}
		for i := 0; i < rv.Len() && i < len(s.Elems); i++ {
			m.pullElem(s.Elems[i], true, nil, s, rv.Index(i), vc, seen, i)
		}
	}
}

// nanKey reports a script-side NaN key (float or complex with a NaN
// component) — CanonicalKey never stores it under a stable key.
func nanKey(k runtime.Value) bool {
	switch x := runtime.Unwrap(k).(type) {
	case float64:
		return x != x
	case float32:
		return x != x
	case complex128:
		return real(x) != real(x) || imag(x) != imag(x)
	case complex64:
		return real(x) != real(x) || imag(x) != imag(x)
	}
	return false
}

// nanReflectKey is nanKey for a host map key.
func nanReflectKey(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float() != rv.Float()
	case reflect.Complex64, reflect.Complex128:
		c := rv.Complex()
		return real(c) != real(c) || imag(c) != imag(c)
	}
	return false
}

// isContainerVal reports whether v unwraps to a script map or slice.
func isContainerVal(v runtime.Value) bool {
	switch runtime.Unwrap(v).(type) {
	case *runtime.Map, *runtime.Slice:
		return true
	}
	return false
}

// pullElem folds one host element/entry into the script container: a
// nested container recurses (keeping the script value's identity and
// registering the new host child), an unchanged scalar keeps its tag,
// anything else is replaced.
func (m *Marshal) pullElem(cur runtime.Value, has bool, sk runtime.Value, parent runtime.Value, ev reflect.Value, vc runtime.VMCaller, seen map[runtime.Value]bool, idxs ...int) {
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
			ce := m.projectedVal(ev)
			if ce == nil {
				ce = &projEntry{sv: inner, rv: ev}
			}
			m.pullProjected(ce, vc, seen)
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
			// a host-added container registers so a callback handed it
			// resolves back to the script value.
			m.registerProjected(nv, ev.Interface())
		}
	case *runtime.Slice:
		p.Elems[idxs[0]] = retagElem(cur, nv)
	}
}

// syncProjected merges the script container's changes into the host
// projection the host keeps reading after the call — template
// {{range}} sees the callback's writes like gc's shared map would.
// Host-side writes survive: deletes apply only to keys the merge knew,
// and a value the script left alone stays untouched. A child
// container re-projects to a fresh host value, which registers so a
// later callback receiving it resolves back.
func (m *Marshal) syncProjected(e *projEntry, vc runtime.VMCaller) {
	rv := e.rv
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return
	}
	switch s := e.sv.(type) {
	case *runtime.Map:
		if rv.Kind() != reflect.Map || rv.IsNil() {
			return
		}
		krt, ert := rv.Type().Key(), rv.Type().Elem()
		for _, mk := range rv.MapKeys() {
			if !e.host[hostKey(mk)] {
				continue // host-added since the merge — keep
			}
			if _, ok := s.Get(GoSlotValueOf(mk)); !ok {
				rv.SetMapIndex(mk, reflect.Value{})
			}
		}
		for i := 0; i < s.Len(); i++ {
			sk, sv := s.At(i)
			if old, ok := e.snap[projKey(sk)]; ok && sameSlot(sv, old) {
				continue // script left this entry alone — keep the host's
			}
			hk, err := m.ToReflect(sk, krt, vc)
			if err != nil {
				continue
			}
			hv, err := m.ToReflect(sv, ert, vc)
			if err != nil {
				continue
			}
			rv.SetMapIndex(hk, hv)
			m.registerProjected(sv, hv.Interface())
		}
		e.host = hostSnap(rv)
		e.snap = scriptSnap(s)
	case *runtime.Slice:
		if rv.Kind() != reflect.Slice {
			return
		}
		if s.Host.IsValid() && s.Host.Pointer() == rv.Pointer() {
			// the script slice's element stores already land on the
			// host backing — pushing stale Elems back would revert
			// writes the callee made through that same array.
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

// syncRegistered mirrors every registered projection's script state
// into its host counterpart. A FuncMap func's writes reach the map by
// script identity — a closure mutating a captured map touches no arg —
// so after each callback every entry syncs, not just the resolved
// ones.
func (m *Marshal) syncRegistered(vc runtime.VMCaller) {
	st, ok := m.ownStack()
	if !ok {
		return
	}
	s := st
	s.mu.Lock()
	var es []*projEntry
	for i := range s.scopes {
		for _, e := range s.scopes[i] {
			es = append(es, e)
		}
	}
	s.mu.Unlock()
	for _, e := range es {
		m.syncProjected(e, vc)
	}
}

// pullUsedEntries runs one last host->script merge on every consumed
// projection before the call's write-back finishes — a host write that
// landed after the last callback (or on a child only a callback held,
// never the arg itself) still reaches the script value.
func (m *Marshal) pullUsedEntries(vc runtime.VMCaller) {
	st, ok := m.ownStack()
	if !ok {
		return
	}
	s := st
	s.mu.Lock()
	var es []*projEntry
	for i := range s.scopes {
		for p, e := range s.scopes[i] {
			if s.used[p] {
				es = append(es, e)
			}
		}
	}
	s.mu.Unlock()
	seen := map[runtime.Value]bool{}
	for _, e := range es {
		m.pullProjected(e, vc, seen)
	}
}

// projectedUsed reports whether a callback on this goroutine consumed
// the projection with this pointer — its script value is authoritative
// now, so write-back must not restore the host copy's stale contents.
func (m *Marshal) projectedUsed(p uintptr) bool {
	st, ok := m.ownStack()
	if !ok {
		return false
	}
	s := st
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[p]
}
