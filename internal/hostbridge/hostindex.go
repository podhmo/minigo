// hostindex.go indexes and mutates boxed host containers the way the
// script index/slice operators do: host map/slice/string element reads,
// index writes, delete/clear, and lo:hi:max slicing with Go's bounds
// semantics. The VM-side operations they need (trap, materialize,
// bound checks) arrive through IndexHooks.
package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// IndexHooks supplies the VM operations host index/slice ops need; the
// vm package fills one per frame.
type IndexHooks struct {
	// VC is the calling VM, for marshal conversions.
	VC runtime.VMCaller
	// Trap panics a script trap carrying the frame's position.
	Trap func(format string, args ...any)
	// Materialize resolves constants and operand values.
	Materialize func(x runtime.Value) runtime.Value
	// BoundUnsigned reports whether the operand's domain is unsigned.
	BoundUnsigned func(x runtime.Value) bool
	// Bounds reads lo/hi slice bounds for a container of length n.
	Bounds func(lo, hi runtime.Value, n int64) (BoundOperand, BoundOperand)
	// MaxBound reads a 3-index slice's max bound.
	MaxBound func(max runtime.Value, capN int64) BoundOperand
}

// hostIndex reads b[idx] on a boxed host map, slice, array or string. ok
// is false when b holds none of those; found is the map comma-ok.
func (m *Marshal) HostIndex(hk *IndexHooks, b *runtime.GoValue, idx runtime.Value) (val runtime.Value, found, ok bool) {
	rv := reflect.ValueOf(b.V)
	switch rv.Kind() {
	case reflect.Map:
		ev := rv.MapIndex(m.HostMapKey(hk, rv, idx))
		if !ev.IsValid() {
			return GoValueOf(reflect.Zero(rv.Type().Elem())), false, true
		}
		return GoValueOf(ev), true, true
	case reflect.Slice, reflect.Array, reflect.String:
		return GoValueOf(rv.Index(m.HostIndexInt(hk, rv, idx))), true, true
	}
	return nil, false, false
}

// hostIndexInt reads idx as an in-range index into the host slice, array
// or string rv, panicking like a script slice index when it is out of
// range (wide unsigned indices included).
func (m *Marshal) HostIndexInt(hk *IndexHooks, rv reflect.Value, idx runtime.Value) int {
	ix := runtime.Unwrap(hk.Materialize(idx))
	i, isInt := IndexSmallInt(ix)
	if !isInt {
		if u, isU := UintOperand(ix); isU {
			panic(runtime.BoundsPanic(u, rv.Len()))
		}
		hk.Trap("%s index is %T", rv.Type(), idx)
	}
	if i < 0 && hk.BoundUnsigned(idx) {
		panic(runtime.BoundsPanic(uint64(i), rv.Len()))
	}
	if i < 0 || i >= int64(rv.Len()) {
		panic(runtime.BoundsPanic(i, rv.Len()))
	}
	return int(i)
}

// hostMapKey converts idx to the key type of the host map rv.
func (m *Marshal) HostMapKey(hk *IndexHooks, rv reflect.Value, idx runtime.Value) reflect.Value {
	kv, err := m.ToReflect(hk.Materialize(idx), rv.Type().Key(), hk.VC)
	if err != nil {
		hk.Trap("index %s with %T: %v", rv.Type(), idx, err)
	}
	return kv
}

// hostSetIndex writes b[idx] = val on a boxed host map or slice
// (http.Header, url.Values, net.IP), converting val to the element type.
// ok is false when b holds neither; a host array value is a copy, so
// writing it is not offered.
func (m *Marshal) HostSetIndex(hk *IndexHooks, b *runtime.GoValue, idx, val runtime.Value) bool {
	rv := reflect.ValueOf(b.V)
	var dst reflect.Value
	var key reflect.Value
	switch rv.Kind() {
	case reflect.Map:
		key = m.HostMapKey(hk, rv, idx)
		if rv.IsNil() {
			panic(runtime.NilMapAssignPanic())
		}
	case reflect.Slice:
		dst = rv.Index(m.HostIndexInt(hk, rv, idx))
	default:
		return false
	}
	ev, err := m.ToReflect(hk.Materialize(val), rv.Type().Elem(), hk.VC)
	if err != nil {
		hk.Trap("cannot use %T as %s value in assignment: %v", val, rv.Type().Elem(), err)
	}
	if rv.Kind() == reflect.Map {
		rv.SetMapIndex(key, ev)
	} else {
		dst.Set(ev)
	}
	return true
}

// hostElemRef returns b[idx] as a box over the host value itself when
// the element is a slice or map, so a nested write (q["tag"][0] = v,
// h[k][i] = v) reaches host storage the way Go's shared backing does. ok
// is false for any other base or element shape.
func (m *Marshal) HostElemRef(hk *IndexHooks, b *runtime.GoValue, idx runtime.Value) (runtime.Value, bool) {
	rv := reflect.ValueOf(b.V)
	var ev reflect.Value
	switch rv.Kind() {
	case reflect.Map:
		if k := rv.Type().Elem().Kind(); k != reflect.Slice && k != reflect.Map {
			return nil, false
		}
		ev = rv.MapIndex(m.HostMapKey(hk, rv, idx))
		if !ev.IsValid() {
			ev = reflect.Zero(rv.Type().Elem()) // missing key: the nil element
		}
	case reflect.Slice:
		if k := rv.Type().Elem().Kind(); k != reflect.Slice && k != reflect.Map {
			return nil, false
		}
		ev = rv.Index(m.HostIndexInt(hk, rv, idx))
	default:
		return nil, false
	}
	return &runtime.GoValue{V: ev.Interface()}, true
}

// HostMapDelete runs the delete builtin on a boxed host map; ok is false
// when m does not hold a map.
func (m *Marshal) HostMapDelete(hk *IndexHooks, mv *runtime.GoValue, key runtime.Value) (ok bool) {
	rv := reflect.ValueOf(mv.V)
	if rv.Kind() != reflect.Map {
		return false
	}
	kv := m.HostMapKey(hk, rv, key)
	if !rv.IsNil() {
		rv.SetMapIndex(kv, reflect.Value{})
	}
	return true
}

// HostClear runs the clear builtin on a boxed host map or slice; ok is
// false when m holds neither.
func (m *Marshal) HostClear(mv *runtime.GoValue) (ok bool) {
	rv := reflect.ValueOf(mv.V)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		rv.Clear()
		return true
	}
	return false
}

// hostSlice evaluates b[lo:hi(:max)] on a boxed host slice or string. The
// result keeps the host type (net.IP's ip[:2] is a net.IP), so a named
// slice stays boxed for its methods.
func (m *Marshal) HostSlice(hk *IndexHooks, b *runtime.GoValue, lo, hi, max runtime.Value, three bool) (runtime.Value, bool) {
	rv := reflect.ValueOf(b.V)
	switch rv.Kind() {
	case reflect.Slice:
		l, h := hk.Bounds(lo, hi, int64(rv.Len()))
		cp := int64(rv.Cap())
		if three {
			m := hk.MaxBound(max, cp)
			if r := SliceBoundsReason(l, h, m, cp, true, "capacity"); r != "" {
				panic(runtime.SliceBoundsPanic(r))
			}
			return GoValueOf(rv.Slice3(int(l.Pos()), int(h.Pos()), int(m.Pos()))), true
		}
		if r := SliceBoundsReason(l, h, BoundOperand{}, cp, false, "capacity"); r != "" {
			panic(runtime.SliceBoundsPanic(r))
		}
		return GoValueOf(rv.Slice(int(l.Pos()), int(h.Pos()))), true
	case reflect.String:
		if three {
			hk.Trap("cannot slice a string with 3 indices")
		}
		n := int64(rv.Len())
		l, h := hk.Bounds(lo, hi, n)
		if r := SliceBoundsReason(l, h, BoundOperand{}, n, false, "length"); r != "" {
			panic(runtime.SliceBoundsPanic(r))
		}
		return GoValueOf(rv.Slice(int(l.Pos()), int(h.Pos()))), true
	}
	return nil, false
}
