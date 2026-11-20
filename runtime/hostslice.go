package runtime

import "reflect"

// hostSlice.go: a Slice can alias the host slice it was unboxed from —
// `buf.Bytes()`, an unnamed host slice field, a map element — so an
// element store reaches the host backing like Go's shared array. The
// Slice keeps its script Elems (reads never cross back); writes go
// through SetElem, which commits the host slot when the value
// converts. A conversion the host cannot take (a script struct into a
// host element type) leaves the script store alone, the same
// divergence the unbox had before.

// SetElem writes element i: the script view takes v, and a host
// backing, when present, takes v converted to the element type.
func (s *Slice) SetElem(i int64, v Value) {
	s.Elems[i] = v
	if !s.Host.IsValid() {
		return
	}
	dst := s.Host.Index(int(i))
	if !dst.CanSet() {
		return
	}
	if ev, ok := hostScalarOf(v, dst); ok {
		dst.Set(ev)
	}
}

// CommitHost writes every element through to the host backing — the
// bulk counterpart of SetElem for operations that reorder or rewrite
// Elems in one pass (sort, pack): the host slot takes each converted
// value like a SetElem store. It reports whether every slot took its
// element; a caller about to alias the backing as its result should
// check that — a slot the value could not convert into keeps its old
// contents, so aliasing would hand back stale elements.
func (s *Slice) CommitHost() bool {
	return s.CommitHostRange(int64(len(s.Elems)))
}

// CommitHostRange commits Elems[:hi] the way CommitHost does.
func (s *Slice) CommitHostRange(hi int64) bool {
	if !s.Host.IsValid() {
		return false
	}
	ok := true
	for i := int64(0); i < hi; i++ {
		dst := s.Host.Index(int(i))
		if !dst.CanSet() {
			ok = false
			continue
		}
		if ev, conv := hostScalarOf(s.Elems[i], dst); conv {
			dst.Set(ev)
		} else {
			ok = false
		}
	}
	return ok
}

// HostReslice returns the sub-view of s's host backing a two-index
// reslice shares, or the zero Value when s has no backing.
func (s *Slice) HostReslice(lo, hi int64) reflect.Value {
	if !s.Host.IsValid() || s.Host.Kind() != reflect.Slice {
		return reflect.Value{}
	}
	if lo < 0 || hi > int64(s.Host.Len()) || lo > hi {
		return reflect.Value{}
	}
	return s.Host.Slice(int(lo), int(hi))
}

// HostReslice3 is HostReslice for the three-index form.
func (s *Slice) HostReslice3(lo, hi, max int64) reflect.Value {
	if !s.Host.IsValid() || s.Host.Kind() != reflect.Slice {
		return reflect.Value{}
	}
	if lo < 0 || hi > int64(s.Host.Len()) || lo > hi || max > int64(s.Host.Cap()) || hi > max {
		return reflect.Value{}
	}
	return s.Host.Slice3(int(lo), int(hi), int(max))
}

// hostScalarOf converts a stored script value for the host slot dst:
// basic kinds assign or convert, a host box hands over when assignable
// (or element-assignable, the *T-over-T shape). An interface slot keeps
// the outgoing element's concrete type — the script side stores a host
// int as int64, so writing int64 back would change the host's dynamic
// type (%T, type assertions). Anything else reports unset — the caller
// keeps the script store only.
func hostScalarOf(v Value, dst reflect.Value) (reflect.Value, bool) {
	t := dst.Type()
	if t.Kind() == reflect.Interface && t.NumMethod() == 0 && !dst.IsNil() {
		if ev, ok := hostScalarToType(v, dst.Elem().Type()); ok {
			return ev, true
		}
	}
	return hostScalarToType(v, t)
}

// hostScalarToType is hostScalarOf's conversion against a bare type.
func hostScalarToType(v Value, t reflect.Type) (reflect.Value, bool) {
	for {
		if n, ok := v.(*Named); ok {
			v = n.V
			continue
		}
		break
	}
	switch x := v.(type) {
	case *GoValue:
		sv := reflect.ValueOf(x.V)
		if !sv.IsValid() {
			return reflect.Zero(t), true
		}
		if sv.Type().AssignableTo(t) {
			return sv, true
		}
		if sv.Kind() == reflect.Ptr && !sv.IsNil() && sv.Elem().Type().AssignableTo(t) {
			return sv.Elem(), true
		}
		if sv.Type().ConvertibleTo(t) {
			return sv.Convert(t), true
		}
		return reflect.Value{}, false
	case *TypedNil:
		return reflect.Zero(t), true
	}
	switch t.Kind() {
	case reflect.Interface:
		// an empty-interface slot takes the scalar's Go value as-is;
		// anything else (a script Slice/Map/Struct) stays unwritten.
		if t.NumMethod() == 0 {
			switch x := v.(type) {
			case string:
				return reflect.ValueOf(x), true
			case bool:
				return reflect.ValueOf(x), true
			case int64:
				return reflect.ValueOf(x), true
			case float64:
				return reflect.ValueOf(x), true
			case complex128:
				return reflect.ValueOf(x), true
			}
		}
	case reflect.String:
		if s, ok := v.(string); ok {
			return reflect.ValueOf(s).Convert(t), true
		}
	case reflect.Bool:
		if b, ok := v.(bool); ok {
			return reflect.ValueOf(b).Convert(t), true
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if i, ok := v.(int64); ok {
			ev := reflect.New(t).Elem()
			if ev.OverflowInt(i) {
				return reflect.Value{}, false
			}
			ev.SetInt(i)
			return ev, true
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if i, ok := v.(int64); ok {
			ev := reflect.New(t).Elem()
			if i < 0 || ev.OverflowUint(uint64(i)) {
				return reflect.Value{}, false
			}
			ev.SetUint(uint64(i))
			return ev, true
		}
	case reflect.Float32, reflect.Float64:
		if f, ok := v.(float64); ok {
			ev := reflect.New(t).Elem()
			if ev.OverflowFloat(f) {
				return reflect.Value{}, false
			}
			ev.SetFloat(f)
			return ev, true
		}
	case reflect.Complex64, reflect.Complex128:
		if c, ok := v.(complex128); ok {
			ev := reflect.New(t).Elem()
			if ev.OverflowComplex(c) {
				return reflect.Value{}, false
			}
			ev.SetComplex(c)
			return ev, true
		}
	}
	return reflect.Value{}, false
}
