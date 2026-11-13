package minigo

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

// Boxed host named slices (a net.IP) through the builtins and the slices
// and sort intrinsics. They keep their box for method dispatch; the
// operations below either act on the host backing (append, copy, make,
// sort.Slice) or read the slice as a script snapshot, run the script
// implementation and write the result back (see hostSliceBoundary).

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
	rt := vm.HostNilSliceType(td)
	if rt == nil {
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

// makeHostType is make(T, n, cap) of a bound host slice or map type
// (net.IP, url.Values). ok is false for any other type.
func makeHostType(td *runtime.TypeDef, args []runtime.Value) (runtime.Value, bool) {
	if td.HostNew == nil {
		return nil, false
	}
	rt := td.HostType()
	if rt == nil {
		return nil, false
	}
	switch rt.Kind() {
	case reflect.Slice:
		n := int64(0)
		if len(args) > 1 {
			n = int64Of(runtime.Unwrap(constNative(args[1])))
		}
		c := n
		if len(args) > 2 {
			c = int64Of(runtime.Unwrap(constNative(args[2])))
		}
		if n < 0 || n > math.MaxInt32 {
			panic(runtime.MakeslicePanic("len"))
		}
		if c < n || c > math.MaxInt32 {
			panic(runtime.MakeslicePanic("cap"))
		}
		return runtime.Tag(td, &runtime.GoValue{V: reflect.MakeSlice(rt, int(n), int(c)).Interface()}), true
	case reflect.Map:
		return runtime.Tag(td, &runtime.GoValue{V: reflect.MakeMap(rt).Interface()}), true
	}
	return nil, false
}

// hostSortSlice runs sort.Slice / sort.SliceStable on a boxed host slice:
// the host slice itself is reordered while the script less function reads
// it by index, as the slice the script passed. ok is false when x is no
// host slice.
func hostSortSlice(v runtime.VMCaller, x, less runtime.Value, stable bool) bool {
	_, rv, ok := vm.HostSliceOf(x)
	if !ok {
		return false
	}
	cmp := func(i, j int) bool {
		r := callOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
		b, _ := r.(bool)
		return b
	}
	if stable {
		sort.SliceStable(rv.Interface(), cmp)
	} else {
		sort.Slice(rv.Interface(), cmp)
	}
	return true
}

// hostSliceUse describes where the slices a function reads sit among its
// arguments (pos; nil means every argument) and whether it reorders them
// in place.
type hostSliceUse struct {
	pos     []int
	inPlace bool
}

var (
	firstSliceRead     = hostSliceUse{pos: []int{0}}
	firstSliceReorder  = hostSliceUse{pos: []int{0}, inPlace: true}
	firstTwoSlicesRead = hostSliceUse{pos: []int{0, 1}}
)

// slicesHostUse is the exception list of the slices package: every other
// function reads a slice from its first argument.
var slicesHostUse = map[string]hostSliceUse{
	"Concat":         {},
	"Equal":          firstTwoSlicesRead,
	"EqualFunc":      firstTwoSlicesRead,
	"Compare":        firstTwoSlicesRead,
	"CompareFunc":    firstTwoSlicesRead,
	"Sort":           firstSliceReorder,
	"SortFunc":       firstSliceReorder,
	"SortStableFunc": firstSliceReorder,
	"Reverse":        firstSliceReorder,
	"Compact":        firstSliceReorder,
	"CompactFunc":    firstSliceReorder,
	"Delete":         firstSliceReorder,
	"DeleteFunc":     firstSliceReorder,
}

// sortHostUse names the sort functions that take a plain slice argument
// (sort.Sort and friends take a sort.Interface, which a box satisfies
// as it is).
var sortHostUse = map[string]hostSliceUse{
	"Ints":     firstSliceReorder,
	"Float64s": firstSliceReorder,
	"Strings":  firstSliceReorder,
}

// hostSliceBoundary wraps the intrinsics of pkg so a boxed host slice
// among their slice arguments reads as a script slice: the intrinsic runs
// on a snapshot, an in-place one writes the reordered elements back to the
// host backing, and a slice result of the snapshot's type is boxed as the
// host type again (slices.Clone of a net.IP is a net.IP). With def set it
// also covers every function of pkg that uses leaves out; otherwise only
// the functions uses names.
func hostSliceBoundary(pkg map[string]runtime.Value, uses map[string]hostSliceUse, def *hostSliceUse) map[string]runtime.Value {
	for name, val := range pkg {
		bf, ok := val.(*runtime.BuiltinFunc)
		if !ok || bf.Fn == nil || bf.GenFn != nil {
			continue
		}
		use, named := uses[name]
		if !named {
			if def == nil {
				continue
			}
			use = *def
		}
		wrapped := *bf
		wrapped.Fn = hostSliceCall(bf.Fn, use)
		pkg[name] = &wrapped
	}
	return pkg
}

func hostSliceCall(fn func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), use hostSliceUse) func(runtime.VMCaller, []runtime.Value) (runtime.Value, error) {
	return func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		var snaps []runtime.Value // parallel to args; nil where no host slice
		orig := args
		for i := range args {
			if use.pos != nil && !slices.Contains(use.pos, i) {
				continue
			}
			s, ok := vm.UnboxHostSlice(orig[i])
			if !ok {
				continue
			}
			if snaps == nil {
				snaps = make([]runtime.Value, len(orig))
				args = slices.Clone(orig) // the caller's buffer is not ours to rewrite
			}
			snaps[i], args[i] = s, s
		}
		if snaps == nil {
			return fn(vc, args)
		}
		res, err := fn(vc, args)
		if err != nil {
			return nil, err
		}
		for i, s := range snaps {
			if s == nil {
				continue
			}
			if use.inPlace {
				if err := vm.StoreHostSlice(vc, orig[i], s); err != nil {
					return nil, err
				}
			}
		}
		for i, s := range snaps {
			if s != nil {
				if r := vm.ReboxHostSlice(vc, orig[i], s, res); r != res {
					return r, nil
				}
			}
		}
		return res, nil
	}
}
