package builtin

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

// hostSliceBase is the host slice an append starts from: a boxed one, or
// the typed nil of a bound host slice type (`var ip net.IP`).
func hostSliceBase(x runtime.Value) (runtime.Value, bool) {
	if _, _, ok := vm.HostSliceOf(x); ok {
		return x, true
	}
	var td *runtime.TypeDef
	switch n := runtime.Unwrap(x).(type) {
	case *runtime.TypedNil:
		td = n.Typ
	case *runtime.IfaceNil:
		td = n.Typ
	}
	rt := hostbridge.HostNilType(td)
	if rt == nil || rt.Kind() != reflect.Slice {
		return nil, false
	}
	zero := &runtime.GoValue{V: reflect.Zero(rt).Interface()}
	if td.HostNew != nil {
		return runtime.Tag(td, zero), true // a script-declared net.IP keeps its tag
	}
	return zero, true
}

// spreadElems is the element list of an append operand list whose last
// operand may be a spread marker.
func spreadElems(args []runtime.Value) ([]runtime.Value, error) {
	n := len(args)
	if n == 0 {
		return args, nil
	}
	sp, ok := args[n-1].(*runtime.Spread)
	if !ok {
		return args, nil
	}
	if sp.S.Virtual() {
		// a huge zero-size-element slice carries only its length
		return nil, fmt.Errorf("append of a %d-element virtual slice to a host slice", sp.S.Len())
	}
	return append(slices.Clone(args[:n-1]), sp.S.Elems...), nil
}

// appendHostSlice is append on a boxed host slice: the host slice grows
// itself, so the result keeps its host type.
func appendHostSlice(v runtime.VMCaller, base runtime.Value, add []runtime.Value) (runtime.Value, error) {
	add, err := spreadElems(add)
	if err != nil {
		return nil, err
	}
	return vm.AppendHostSlice(v, base, add)
}

// copyIntoHostSlice is copy(dst, src) with a boxed host slice dst; src
// holds the elements already read from a script slice, a host slice
// snapshot or a string.
func copyIntoHostSlice(v runtime.VMCaller, dst runtime.Value, src []runtime.Value) (runtime.Value, error) {
	n, err := vm.CopyIntoHostSlice(v, dst, src)
	if err != nil {
		return nil, err
	}
	return int64(n), nil
}
