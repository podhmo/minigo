package vm

import (
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// A boxed host named slice (a net.IP handed in by the embedder or returned
// by a host call) keeps its box for method dispatch. The builtins and
// intrinsics that only know script slices reach it through the functions
// below: read it as a script slice (UnboxHostSlice), write the result of
// an in-place operation back (StoreHostSlice), and give a slice result of
// the same type its host type again (ReboxHostSlice). append and copy
// operate on the host backing directly.

// HostSliceOf reports whether x — a bare box, a variable cell holding
// one, or a Named tag over either — carries a host slice, and returns the
// box and the slice itself.
func HostSliceOf(x runtime.Value) (*runtime.GoValue, reflect.Value, bool) {
	if c, ok := x.(*runtime.Cell); ok {
		x = c.Elem
	}
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	gv, ok := x.(*runtime.GoValue)
	if !ok {
		return nil, reflect.Value{}, false
	}
	rv := reflect.ValueOf(gv.V)
	if rv.Kind() != reflect.Slice {
		return nil, reflect.Value{}, false
	}
	return gv, rv, true
}

// UnboxHostSlice reads a boxed host slice as the script slice of the same
// shape (an unnamed []E, as for any host slice that is not named): a
// snapshot — element writes into it do not reach the host. A nil host
// slice reads as a typed nil. ok is false when x holds no host slice.
func UnboxHostSlice(x runtime.Value) (runtime.Value, bool) {
	_, rv, ok := HostSliceOf(x)
	if !ok {
		return nil, false
	}
	if rv.Type().Name() != "" {
		rv = rv.Convert(reflect.SliceOf(rv.Type().Elem()))
	}
	return goValueOf(rv), true
}

// hostSliceLike boxes hv, a host slice derived from host, the way host was
// boxed: a Named tag stays on the result.
func hostSliceLike(host runtime.Value, hv reflect.Value) runtime.Value {
	if c, ok := host.(*runtime.Cell); ok {
		host = c.Elem
	}
	box := &runtime.GoValue{V: hv.Interface()}
	if n, ok := host.(*runtime.Named); ok {
		return runtime.Tag(n.Typ, box)
	}
	return box
}

// StoreHostSlice writes the elements of snap — a snapshot UnboxHostSlice
// returned for host that an in-place operation reordered — back into the
// host slice's own backing.
func StoreHostSlice(vc runtime.VMCaller, host, snap runtime.Value) error {
	_, rv, ok := HostSliceOf(host)
	s, isSlice := snap.(*runtime.Slice)
	if !ok || !isSlice {
		return nil
	}
	et := rv.Type().Elem()
	for i := 0; i < len(s.Elems) && i < rv.Len(); i++ {
		ev, err := toReflectValue(s.Elems[i], et, vc)
		if err != nil {
			return err
		}
		rv.Index(i).Set(ev)
	}
	return nil
}

// ReboxHostSlice gives res, a slice result an intrinsic computed from
// snap (the snapshot of host), the host slice type again when it is a
// slice of the snapshot's own type: slices.Clone(ip) is a net.IP. A result
// that shares the snapshot's backing (slices.Compact, Delete) views the
// host backing the same way, so later writes reach it; any other result
// is a fresh host slice. Everything else passes through.
func ReboxHostSlice(vc runtime.VMCaller, host, snap, res runtime.Value) runtime.Value {
	_, rv, ok := HostSliceOf(host)
	sn, snOK := snap.(*runtime.Slice)
	rs, resOK := res.(*runtime.Slice)
	if !ok || !snOK || !resOK || rs.Typ != sn.Typ || rs.Virtual() {
		return res
	}
	if len(rs.Elems) > 0 {
		for off := range sn.Elems {
			if &sn.Elems[off] == &rs.Elems[0] && off+len(rs.Elems) <= rv.Len() {
				return hostSliceLike(host, rv.Slice(off, off+len(rs.Elems)))
			}
		}
	}
	hv, err := toReflectValue(rs, rv.Type(), vc)
	if err != nil {
		return res
	}
	return hostSliceLike(host, hv.Convert(rv.Type()))
}

// AppendHostSlice is append(host, add...) on a boxed host slice: it grows
// the host slice itself, so an append within capacity writes the shared
// backing as in Go, and the result keeps the host type.
func AppendHostSlice(vc runtime.VMCaller, host runtime.Value, add []runtime.Value) (runtime.Value, error) {
	_, rv, ok := HostSliceOf(host)
	if !ok {
		return nil, fmt.Errorf("append on %T", host)
	}
	et := rv.Type().Elem()
	vals := make([]reflect.Value, len(add))
	for i, a := range add {
		ev, err := toReflectValue(a, et, vc)
		if err != nil {
			return nil, fmt.Errorf("cannot use %T as %s value in append: %w", a, et, err)
		}
		vals[i] = ev
	}
	return hostSliceLike(host, reflect.Append(rv, vals...)), nil
}

// CopyIntoHostSlice is copy(host, src) with src already read as elements:
// it writes the host backing and reports how many elements it copied.
func CopyIntoHostSlice(vc runtime.VMCaller, host runtime.Value, src []runtime.Value) (int, error) {
	_, rv, ok := HostSliceOf(host)
	if !ok {
		return 0, fmt.Errorf("copy on %T", host)
	}
	n := min(rv.Len(), len(src))
	et := rv.Type().Elem()
	vals := make([]reflect.Value, n)
	for i := range vals {
		ev, err := toReflectValue(src[i], et, vc)
		if err != nil {
			return 0, fmt.Errorf("cannot use %T as %s value in copy: %w", src[i], et, err)
		}
		vals[i] = ev
	}
	for i, ev := range vals {
		rv.Index(i).Set(ev)
	}
	return n, nil
}

// hostSliceConv converts x to the host slice type of td (a bound named
// slice such as net.IP): from a host slice of a convertible type, a script
// slice, or — for a byte slice type — a string. Conversions between host
// slice types share the backing as in Go; a script slice or a string is
// copied. ok is false when td is no host slice type or x has no such
// source.
func (v *VM) hostSliceConv(td *runtime.TypeDef, x runtime.Value) (runtime.Value, bool, error) {
	if td.HostNew == nil {
		return nil, false, nil
	}
	rt := td.HostType()
	if rt == nil || rt.Kind() != reflect.Slice {
		return nil, false, nil
	}
	if _, rv, ok := HostSliceOf(x); ok {
		if rv.Type() == rt {
			return x, true, nil
		}
		if !rv.Type().ConvertibleTo(rt) {
			return nil, false, nil
		}
		return runtime.Tag(td, &runtime.GoValue{V: rv.Convert(rt).Interface()}), true, nil
	}
	switch s := x.(type) {
	case *runtime.Slice:
		hv, err := toReflectValue(s, rt, v)
		if err != nil {
			return nil, true, err
		}
		// an unnamed []E is assignable as it is; the conversion names it
		return runtime.Tag(td, &runtime.GoValue{V: hv.Convert(rt).Interface()}), true, nil
	case string:
		if rt.Elem().Kind() == reflect.Uint8 {
			return runtime.Tag(td, &runtime.GoValue{V: reflect.ValueOf([]byte(s)).Convert(rt).Interface()}), true, nil
		}
	}
	return nil, false, nil
}

// hostSliceLiteral builds the literal td{...} of a host slice type
// (`net.IP{1, 2, 3, 4}`): positional elements, or `index: element` pairs
// when kv, converted to the element type.
func (v *VM) hostSliceLiteral(f *frame, td *runtime.TypeDef, rt reflect.Type, n int, kv bool, raw []runtime.Value) runtime.Value {
	et := rt.Elem()
	type slot struct {
		idx int
		val reflect.Value
	}
	slots := make([]slot, 0, n)
	next, size := 0, 0
	for i := 0; i < n; i++ {
		e := raw[i]
		if kv {
			k := runtime.Unwrap(v.materialize(f, raw[2*i]))
			idx, ok := indexSmallInt(k)
			if !ok || idx < 0 {
				f.trap("%s literal index is %T", td.Name, raw[2*i])
			}
			next, e = int(idx), raw[2*i+1]
		}
		ev, err := toReflectValue(e, et, v)
		if err != nil {
			f.trap("cannot use %T as %s value in %s literal: %v", e, et, td.Name, err)
		}
		slots = append(slots, slot{next, ev})
		next++
		size = max(size, next)
	}
	sv := reflect.MakeSlice(rt, size, size)
	for _, s := range slots {
		sv.Index(s.idx).Set(s.val)
	}
	return runtime.Tag(td, &runtime.GoValue{V: sv.Interface()})
}
