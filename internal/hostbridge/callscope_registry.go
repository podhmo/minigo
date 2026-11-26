package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

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
	defer s.mu.Unlock()
	if s.merging > 0 {
		// a merge re-projects containers to fresh host values; tracking
		// each fresh projection would grow the merge set every pass —
		// the next callback would then merge an ever-larger list
		// (issue #1399).
		return
	}
	if n := len(s.scopes); n > 0 {
		s.scopes[n-1][rv.Pointer()] = e
		if reg != nil {
			s.slices = append(s.slices, *reg)
		}
	}
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
