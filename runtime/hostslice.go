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
	if ev, ok := hostScalarOf(v, dst.Type()); ok {
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
		if ev, conv := hostScalarOf(s.Elems[i], dst.Type()); conv {
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

// hostScalarOf converts a stored script value to a host element type:
// basic kinds assign or convert, a host box hands over when assignable
// (or element-assignable, the *T-over-T shape). Anything else reports
// unset — the caller keeps the script store only.
func hostScalarOf(v Value, t reflect.Type) (reflect.Value, bool) {
	// tag keeps the first basic-type tag the Named chain carries: a
	// host int8 unboxes as Named{int8, int64(5)}, so writing back into
	// an interface slot must restore int8, not the gc default int —
	// the host's dynamic type (%T, type asserts) would otherwise change
	// on a no-op round-trip. A tag naming a user type (Celsius) is not
	// basic and falls through to the default.
	tag := ""
	for {
		if n, ok := v.(*Named); ok {
			if tag == "" && n.Typ != nil && basicIfaceKinds[n.Typ.Name] != reflect.Invalid {
				tag = n.Typ.Name
			}
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
		// an empty-interface slot takes the scalar under gc's default
		// type for it — the script side stores an int literal as int64
		// where gc boxes `x` in `[]any` as int, so writing int64 back
		// would change the host's dynamic type (%T, type assertions).
		// Anything else (a script Slice/Map/Struct) stays unwritten.
		if t.NumMethod() == 0 {
			switch x := v.(type) {
			case string:
				return reflect.ValueOf(x), true
			case bool:
				return reflect.ValueOf(x), true
			case int64:
				// the tag is honored only when its kind matches the
				// payload family — a `type float32 int` shadow names a
				// float kind for an int payload, and an unsigned tag's
				// negative int64 is the host's high-bit value in two's
				// complement (uint(1)<<63 unboxes as -2^63), not a
				// negative number.
				k := basicIfaceKinds[tag]
				switch {
				case k >= reflect.Int && k <= reflect.Int64:
					ev := reflect.New(intKindType[k]).Elem()
					if ev.OverflowInt(x) {
						return reflect.Value{}, false
					}
					ev.SetInt(x)
					return ev, true
				case k >= reflect.Uint && k <= reflect.Uintptr:
					ev := reflect.New(intKindType[k]).Elem()
					if ev.OverflowUint(uint64(x)) {
						return reflect.Value{}, false
					}
					ev.SetUint(uint64(x))
					return ev, true
				default:
					ev := reflect.New(intKindType[reflect.Int]).Elem()
					if ev.OverflowInt(x) {
						return reflect.Value{}, false
					}
					ev.SetInt(x)
					return ev, true
				}
			case float64:
				k := basicIfaceKinds[tag]
				if k != reflect.Float32 && k != reflect.Float64 {
					k = reflect.Float64
				}
				return reflect.ValueOf(x).Convert(floatKindType[k]), true
			case complex128:
				k := basicIfaceKinds[tag]
				if k != reflect.Complex64 && k != reflect.Complex128 {
					k = reflect.Complex128
				}
				return reflect.ValueOf(x).Convert(complexKindType[k]), true
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

// basicIfaceKinds maps a basic type name to its reflect kind — the
// subset a Named tag can carry that changes an empty-interface write.
var basicIfaceKinds = map[string]reflect.Kind{
	"int": reflect.Int, "int8": reflect.Int8, "int16": reflect.Int16,
	"int32": reflect.Int32, "int64": reflect.Int64, "rune": reflect.Int32,
	"uint": reflect.Uint, "uint8": reflect.Uint8, "uint16": reflect.Uint16,
	"uint32": reflect.Uint32, "uint64": reflect.Uint64, "uintptr": reflect.Uintptr,
	"byte":    reflect.Uint8,
	"float32": reflect.Float32, "float64": reflect.Float64,
	"complex64": reflect.Complex64, "complex128": reflect.Complex128,
}

// intKindType / floatKindType / complexKindType map a reflect kind to
// its concrete type for an interface write.
var intKindType = map[reflect.Kind]reflect.Type{
	reflect.Int: reflect.TypeOf(int(0)), reflect.Int8: reflect.TypeOf(int8(0)),
	reflect.Int16: reflect.TypeOf(int16(0)), reflect.Int32: reflect.TypeOf(int32(0)),
	reflect.Int64: reflect.TypeOf(int64(0)),
	reflect.Uint:  reflect.TypeOf(uint(0)), reflect.Uint8: reflect.TypeOf(uint8(0)),
	reflect.Uint16: reflect.TypeOf(uint16(0)), reflect.Uint32: reflect.TypeOf(uint32(0)),
	reflect.Uint64: reflect.TypeOf(uint64(0)), reflect.Uintptr: reflect.TypeOf(uintptr(0)),
}

var floatKindType = map[reflect.Kind]reflect.Type{
	reflect.Float32: reflect.TypeOf(float32(0)), reflect.Float64: reflect.TypeOf(float64(0)),
}

var complexKindType = map[reflect.Kind]reflect.Type{
	reflect.Complex64: reflect.TypeOf(complex64(0)), reflect.Complex128: reflect.TypeOf(complex128(0)),
}
