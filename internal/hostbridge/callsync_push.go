package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// syncProjected merges the script container's changes into the host
// projection the host keeps reading after the call — template
// {{range}} sees the callback's writes like gc's shared map would.
// Host-side writes survive: deletes apply only to keys the merge knew,
// and a value the script left alone stays untouched. A child container
// already registered on this goroutine keeps its canonical host projection;
// otherwise its parent would point at an unregistered copy.
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
			var hk reflect.Value
			if krt.Kind() == reflect.Interface {
				// interface keys use the same canonical form as the initial
				// conversion: a script int must remain the int64 key already
				// present, not DeepHost's host-int form.
				hk = reflect.New(krt).Elem()
				if uk := runtime.UnwrapKey(sk); uk != nil {
					hk.Set(reflect.ValueOf(uk))
				}
			} else {
				var err error
				hk, err = m.ToReflect(sk, krt, vc)
				if err != nil {
					continue
				}
			}
			hv, reused := m.projectedReflectValue(sv, ert)
			if !reused {
				var err error
				hv, err = m.ToReflect(sv, ert, vc)
				if err != nil {
					continue
				}
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
			ev, reused := m.projectedReflectValue(s.Elems[i], ert)
			if !reused {
				var err error
				ev, err = m.ToReflect(s.Elems[i], ert, vc)
				if err != nil {
					continue
				}
			}
			if el := rv.Index(i); el.CanSet() {
				el.Set(ev)
				m.registerProjected(s.Elems[i], el.Interface())
			}
		}
	}
}
