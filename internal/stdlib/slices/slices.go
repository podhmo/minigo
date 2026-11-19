package slices

import (
	"fmt"
	"math"
	gslices "slices"
	gsort "sort"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, typedSeq func(*runtime.BuiltinFunc, ...*runtime.TypeDef) runtime.Value) {
	bind("slices", stdlib.GenericFns(stdlib.HostSliceBoundary(stdlib.SlicesSeqHelpers(typedSeq, map[string]runtime.Value{
		"Sort": stdlib.SortInPlace("slices.Sort"),
		"Contains": &runtime.BuiltinFunc{Name: "slices.Contains", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			i, err := stdlib.SliceIndexScript(vc, "slices.Contains", args)
			return i >= 0, err
		}},
		"Index": &runtime.BuiltinFunc{Name: "slices.Index", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			i, err := stdlib.SliceIndexScript(vc, "slices.Index", args)
			return int64(i), err
		}},
		"Clone": &runtime.BuiltinFunc{Name: "slices.Clone", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Clone needs 1 arg")
			}
			switch s := args[0].(type) {
			case *runtime.TypedNil:
				// Clone(nil) is the same typed nil — a host Clone on a nil
				// []any would surface an empty non-nil slice.
				return s, nil
			case *runtime.Slice:
				// keep the declared slice type: a clone boxed as []any loses
				// `[]string` and `sort.Strings(c)` traps on the reuse.
				return &runtime.Slice{Elems: stdlib.CloneElems(s.Elems), Typ: s.Typ}, nil
			case *runtime.Named:
				if _, ok := s.V.(*runtime.TypedNil); ok {
					return s, nil
				}
				if sl, ok := s.V.(*runtime.Slice); ok {
					return runtime.Tag(s.Typ, &runtime.Slice{Elems: stdlib.CloneElems(sl.Elems), Typ: sl.Typ}), nil
				}
			}
			return nil, fmt.Errorf("slices.Clone: arg must be a slice")
		}},
		"Concat": slicesConcat(),
		"Equal": &runtime.BuiltinFunc{Name: "slices.Equal", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.Equal needs 2 args, got %d", len(args))
			}
			xs, xt, okx := stdlib.ScriptSliceArg(vc, args[0])
			ys, yt, oky := stdlib.ScriptSliceArg(vc, args[1])
			if !okx || !oky {
				return gslices.Equal(stdlib.AnySlice(hostbridge.GoNative(args[0])), stdlib.AnySlice(hostbridge.GoNative(args[1]))), nil
			}
			if xt == nil {
				xt = yt
			}
			if len(xs) != len(ys) {
				return false, nil
			}
			for i := range xs {
				if !vc.Equal(xt, xs[i], ys[i]) {
					return false, nil
				}
			}
			return true, nil
		}},
		"IsSorted": h.Fn("slices.IsSorted", func(a []any) (any, error) {
			el := stdlib.ScriptElems(a[0])
			return gsort.SliceIsSorted(el, func(i, j int) bool { return stdlib.LessScript(el[i], el[j]) }), nil
		}),
		"DeleteFunc": &runtime.BuiltinFunc{Name: "slices.DeleteFunc", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.DeleteFunc needs 2 args")
			}
			s, ok := stdlib.SliceOf(args[0])
			if !ok {
				if stdlib.Nilish(args[0]) {
					return args[0], nil // deleting from nil yields nil
				}
				return nil, fmt.Errorf("slices.DeleteFunc: arg must be a slice")
			}
			var out []runtime.Value
			for _, el := range s.Elems {
				r, err := vc.Call(args[1], []runtime.Value{el})
				if err != nil {
					return nil, err
				}
				if drop, _ := r.(bool); !drop {
					out = append(out, el)
				}
			}
			return stdlib.PackSlice(vc, s, out), nil
		}},
		"Compact": &runtime.BuiltinFunc{Name: "slices.Compact", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Compact needs 1 arg")
			}
			s, ok := stdlib.SliceOf(args[0])
			if !ok {
				if stdlib.Nilish(args[0]) {
					return args[0], nil
				}
				return nil, fmt.Errorf("slices.Compact: arg must be a slice")
			}
			_, et, _ := stdlib.ScriptSliceArg(vc, args[0])
			var out []runtime.Value
			for i, el := range s.Elems {
				if i > 0 && vc.Equal(et, out[len(out)-1], el) {
					continue
				}
				out = append(out, el)
			}
			return stdlib.PackSlice(vc, s, out), nil
		}},
		"Repeat": slicesRepeat(),
		"Grow": &runtime.BuiltinFunc{Name: "slices.Grow", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.Grow needs 2 args")
			}
			n := stdlib.Int64Of(args[1])
			if n < 0 {
				panic(&runtime.Panic{Value: "cannot be negative"})
			}
			// log/slog's Record.AddAttrs grows its overflow slice on
			// every LogAttrs call; a nil slice is still nil at n == 0.
			var td *runtime.TypeDef
			var elems []runtime.Value
			switch s := runtime.Unwrap(args[0]).(type) {
			case *runtime.Slice:
				if s.Virtual() || int64(cap(s.Elems)-len(s.Elems)) >= n {
					return args[0], nil
				}
				td, elems = s.Typ, s.Elems
			case *runtime.TypedNil:
				if n == 0 {
					return args[0], nil
				}
				td = s.Typ
			default:
				return nil, fmt.Errorf("slices.Grow: arg must be a slice")
			}
			// gc grows through append — `append(s[:cap(s)],
			// make([]E, n-(cap-len))...)[:len(s)]` — so the new cap
			// follows append's growth rule (cap(Grow(make([]int, 5),
			// 1)) is 10), the same host append the append builtin
			// uses. The old spare keeps its contents; the fresh spare
			// holds zeros, as growslice clears it.
			full := elems[:cap(elems)]
			grown := append(full, make([]runtime.Value, int(n)-(cap(elems)-len(elems)))...)
			// the grown array copies the live elements (gc's growslice
			// memcpy): without it a struct element aliases the old
			// backing, the same bug append's growth had.
			for i := 0; i < len(elems); i++ {
				grown[i] = vc.Copy(grown[i])
			}
			zero := vc.ElemZero(td)
			if runtime.IsZeroSizeValue(zero) {
				grown = grown[:len(grown):len(grown)]
			}
			for i, all := len(full), grown[:cap(grown)]; i < len(all); i++ {
				all[i] = runtime.Copy(zero)
			}
			return stdlib.RetagSlice(args[0], &runtime.Slice{Elems: grown[:len(elems)], Typ: td}), nil
		}},
		"Clip": &runtime.BuiltinFunc{Name: "slices.Clip", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Clip needs 1 arg")
			}
			// s[:len(s):len(s)] — the clipped header shares the
			// backing array, so a later append reallocates instead of
			// writing into the original's spare capacity.
			switch s := runtime.Unwrap(args[0]).(type) {
			case *runtime.Slice:
				if s.Virtual() {
					c := *s
					c.CapN = c.N
					return stdlib.RetagSlice(args[0], &c), nil
				}
				return stdlib.RetagSlice(args[0], &runtime.Slice{Elems: s.Elems[:len(s.Elems):len(s.Elems)], Typ: s.Typ}), nil
			case *runtime.TypedNil:
				return args[0], nil
			}
			return nil, fmt.Errorf("slices.Clip: arg must be a slice")
		}},
		"SortFunc":         stdlib.SortByCmpFunc("slices.SortFunc"),
		"SortStableFunc":   stdlib.SortByCmpFunc("slices.SortStableFunc"),
		"BinarySearch":     slicesBinarySearch(false),
		"BinarySearchFunc": slicesBinarySearch(true),
		"EqualFunc":        slicesEqualFunc(),
		"IndexFunc":        slicesIndexFunc(),
		"ContainsFunc": &runtime.BuiltinFunc{Name: "slices.ContainsFunc", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.ContainsFunc needs 2 args, got %d", len(args))
			}
			s, err := stdlib.SliceParam("slices.ContainsFunc", args[0])
			if err != nil {
				return nil, err
			}
			for _, el := range stdlib.SliceElems(s) {
				if hostbridge.CallBool(vc, args[1], el) {
					return true, nil
				}
			}
			return false, nil
		}},
		"Compare":      slicesCompare("slices.Compare", false),
		"CompareFunc":  slicesCompare("slices.CompareFunc", true),
		"IsSortedFunc": slicesIsSortedFunc(),
		"MaxFunc":      slicesExtremeFunc("slices.MaxFunc", func(c int64) bool { return c > 0 }),
		"MinFunc":      slicesExtremeFunc("slices.MinFunc", func(c int64) bool { return c < 0 }),
		"CompactFunc":  stdlib.SlicesCompactFunc(),
		"Replace":      slicesReplace(),
		"Max": &runtime.BuiltinFunc{Name: "slices.Max", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return stdlib.SliceExtreme(vc, "slices.Max", args, func(best, x runtime.Value) bool { return stdlib.LessScript(best, x) })
		}},
		"Min": &runtime.BuiltinFunc{Name: "slices.Min", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return stdlib.SliceExtreme(vc, "slices.Min", args, func(best, x runtime.Value) bool { return stdlib.LessScript(x, best) })
		}},
		"Reverse": slicesReverse(),
		"Insert":  slicesInsert(),
		"Delete":  slicesDelete(),
	}), stdlib.SlicesHostUse, &stdlib.FirstSliceRead)))
}

// slicesCompare is gslices.Compare (elements ordered with cmp.Compare)
// and gslices.CompareFunc (elements ordered by the script callback): the
// first non-zero element comparison decides, then the shorter slice is
// less. Either slice may be nil.
func slicesCompare(name string, withFunc bool) *runtime.BuiltinFunc {
	want := 2
	if withFunc {
		want = 3
	}
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: withFunc, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != want {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, want, len(args))
		}
		s1, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		s2, err := stdlib.SliceParam(name, args[1])
		if err != nil {
			return nil, err
		}
		x, y := stdlib.SliceElems(s1), stdlib.SliceElems(s2)
		for i, v1 := range x {
			if i >= len(y) {
				return int64(1), nil
			}
			c := int64(0)
			if withFunc {
				c = stdlib.CallInt(vc, args[2], v1, y[i])
			} else {
				c = stdlib.CmpScript(v1, y[i])
			}
			if c != 0 {
				return c, nil
			}
		}
		if len(x) < len(y) {
			return int64(-1), nil
		}
		return int64(0), nil
	}}
}

// slicesIsSortedFunc is gslices.IsSortedFunc: cmp(x[i], x[i-1]) < 0 for
// any i, probed from the end like Go does.
func slicesIsSortedFunc() *runtime.BuiltinFunc {
	const name = "slices.IsSortedFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		x := stdlib.SliceElems(s)
		for i := len(x) - 1; i > 0; i-- {
			if stdlib.CallInt(vc, args[1], x[i], x[i-1]) < 0 {
				return false, nil
			}
		}
		return true, nil
	}}
}

// slicesExtremeFunc is gslices.MaxFunc/MinFunc: the first element wins
// until a later one compares better against it (cmp(x[i], best) passes
// the better test). An empty slice panics like Go.
func slicesExtremeFunc(name string, better func(c int64) bool) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		x := stdlib.SliceElems(s)
		if len(x) == 0 {
			panic(stdlib.EmptyListPanic(name))
		}
		best := x[0]
		for _, el := range x[1:] {
			if better(stdlib.CallInt(vc, args[1], el, best)) {
				best = el
			}
		}
		return best, nil
	}}
}

// slicesCompactFunc is gslices.CompactFunc: an element is dropped when
// eq(x[i], x[i-1]) holds against the ORIGINAL neighbour, so a
// non-transitive eq behaves as it does in Go. The kept elements move to
// the front of the backing array and the vacated tail is zeroed, like
// gslices.DeleteFunc here.

// slicesReplace is gslices.Replace(s, i, j, v...): Go's own algorithm
// runs over the script slice's backing array (so spare capacity is
// reused and an overlapping v is handled as in Go), and the cleared
// tail is zeroed to the element type's zero value instead of a hole.
func slicesReplace() *runtime.BuiltinFunc {
	const name = "slices.Replace"
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < 3 {
			return nil, fmt.Errorf("%s(slice, i, j, elems...) needs at least 3 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		typ := stdlib.SliceParamTyp(s, args[0])
		vs, err := stdlib.ElemOperands(vc, name, typ, args[3:])
		if err != nil {
			return nil, err
		}
		// a bounds failure panics in the host exactly as it would in Go
		// (the backing array has the script slice's len and cap); the
		// VM wraps it as the runtime.boundsError the script recovers.
		base := stdlib.SliceElems(s)
		out := gslices.Replace(base, int(stdlib.IntOf(args[1])), int(stdlib.IntOf(args[2])), vs...)
		if s == nil && len(out) == 0 {
			return args[0], nil // nothing to put in a nil slice: it stays nil
		}
		// a shrunk result shares the backing array; Go cleared the
		// vacated slots with a hole, the script's slot holds the zero.
		if len(out) < len(base) {
			stdlib.FillHoles(vc, typ, base[len(out):])
		}
		return stdlib.RetagSlice(args[0], &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}

// slicesIndexFunc is gslices.IndexFunc: the first index whose element
// satisfies the predicate, -1 for none or for a nil slice.
func slicesIndexFunc() *runtime.BuiltinFunc {
	const name = "slices.IndexFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		for i, el := range stdlib.SliceElems(s) {
			if hostbridge.CallBool(vc, args[1], el) {
				return int64(i), nil
			}
		}
		return int64(-1), nil
	}}
}

// slicesEqualFunc is gslices.EqualFunc: equal lengths and eq true for
// every pair, probed in order. Either slice may be nil — a nil slice
// equals an empty one.
func slicesEqualFunc() *runtime.BuiltinFunc {
	const name = "slices.EqualFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("%s needs 3 args, got %d", name, len(args))
		}
		s1, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		s2, err := stdlib.SliceParam(name, args[1])
		if err != nil {
			return nil, err
		}
		x, y := stdlib.SliceElems(s1), stdlib.SliceElems(s2)
		if len(x) != len(y) {
			return false, nil
		}
		for i := range x {
			if !hostbridge.CallBool(vc, args[2], x[i], y[i]) {
				return false, nil
			}
		}
		return true, nil
	}}
}

// slicesBinarySearch is gslices.BinarySearch (elements ordered with
// cmp.Compare) and gslices.BinarySearchFunc (ordered by the script cmp
// callback, called as cmp(element, target)): the insertion index and
// whether the target is there. A nil slice yields (0, false).
func slicesBinarySearch(withFunc bool) *runtime.BuiltinFunc {
	name, want := "slices.BinarySearch", 2
	if withFunc {
		name, want = "slices.BinarySearchFunc", 3
	}
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: withFunc, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != want {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, want, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		x, target := stdlib.SliceElems(s), args[1]
		cmpAt := func(i int) int64 {
			if withFunc {
				return stdlib.CallInt(vc, args[2], x[i], target)
			}
			return stdlib.CmpScript(x[i], target)
		}
		i, j := 0, len(x)
		for i < j {
			m := int(uint(i+j) >> 1)
			if cmpAt(m) < 0 {
				i = m + 1
			} else {
				j = m
			}
		}
		found := i < len(x) && cmpAt(i) == 0
		return &runtime.Tuple{Elems: []runtime.Value{int64(i), found}}, nil
	}}
}

// slicesReverse is gslices.Reverse: the elements swap in place; a nil
// slice, or one of zero-size elements (all alike), has nothing to move.
func slicesReverse() *runtime.BuiltinFunc {
	const name = "slices.Reverse"
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		s, err := stdlib.SliceParamAny(name, args[0])
		if err != nil {
			return nil, err
		}
		if s != nil && !s.Virtual() {
			gslices.Reverse(s.Elems)
		}
		return runtime.NIL, nil
	}}
}

// slicesInsert is gslices.Insert(s, i, v...): Go's own algorithm runs
// over the script slice's backing array, so spare capacity is reused and
// a bad index panics with the boundsError the script recovers. Inserting
// into a nil slice builds a new one of the same type; inserting nothing
// returns s itself.
func slicesInsert() *runtime.BuiltinFunc {
	const name = "slices.Insert"
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s(slice, i, elems...) needs at least 2 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		typ := stdlib.SliceParamTyp(s, args[0])
		vs, err := stdlib.ElemOperands(vc, name, typ, args[2:])
		if err != nil {
			return nil, err
		}
		base := stdlib.SliceElems(s)
		out := gslices.Insert(base, stdlib.IntOf(args[1]), vs...)
		if len(vs) == 0 {
			return args[0], nil // the index was checked; s is returned as is
		}
		if cap(out) > cap(base) {
			// the fresh backing copies every element, like gc's
			// growslice memcpy: a struct element must not alias the
			// old array's struct.
			for i, e := range out {
				out[i] = vc.Copy(e)
			}
		}
		// a grown array's spare capacity holds holes where Go has zeros.
		stdlib.FillHoles(vc, typ, out[len(out):cap(out)])
		return stdlib.RetagSlice(args[0], &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}

// slicesDelete is gslices.Delete(s, i, j): the elements move down over
// [i, j) in the backing array and the vacated tail is zeroed, so slices
// re-sliced from the original observe the deletion. A nil slice stays
// nil; a bad range panics with the boundsError the script recovers.
func slicesDelete() *runtime.BuiltinFunc {
	const name = "slices.Delete"
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("%s(slice, i, j) needs 3 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		base := stdlib.SliceElems(s)
		out := gslices.Delete(base, stdlib.IntOf(args[1]), stdlib.IntOf(args[2]))
		if s == nil {
			return args[0], nil // only the empty range [0, 0) passed the bounds check
		}
		typ := stdlib.SliceParamTyp(s, args[0])
		if len(out) < len(base) {
			stdlib.FillHoles(vc, typ, base[len(out):])
		}
		return stdlib.RetagSlice(args[0], &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}

// slicesRepeat is gslices.Repeat(s, n): a new slice (never nil, even for
// a nil s or n == 0) of n copies of s, sized exactly len(s)*n.
func slicesRepeat() *runtime.BuiltinFunc {
	const name = "slices.Repeat"
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := stdlib.SliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		n := stdlib.IntOf(args[1])
		if n < 0 {
			panic(&runtime.Panic{Value: "cannot be negative"})
		}
		el := stdlib.SliceElems(s)
		if len(el) > 0 && n > math.MaxInt/len(el) {
			panic(&runtime.Panic{Value: "the result of (len(x) * count) overflows"})
		}
		out := make([]runtime.Value, 0, len(el)*n)
		for range n {
			for _, v := range el {
				out = append(out, runtime.Copy(v))
			}
		}
		return stdlib.RetagSlice(args[0], &runtime.Slice{Elems: out, Typ: stdlib.SliceParamTyp(s, args[0])}), nil
	}}
}

// slicesConcat is gslices.Concat: the arguments' elements in one new
// slice, typed after the arguments. It is nil when there are no
// elements at all, like Go's (Grow(nil, 0) is nil), and any argument may
// be nil.
func slicesConcat() *runtime.BuiltinFunc {
	const name = "slices.Concat"
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		var (
			typ   *runtime.TypeDef
			first runtime.Value
			size  int
			parts = make([][]runtime.Value, len(args))
		)
		for i, a := range args {
			s, err := stdlib.SliceParam(name, a)
			if err != nil {
				return nil, err
			}
			parts[i] = stdlib.SliceElems(s)
			size += len(parts[i])
			if t := stdlib.SliceParamTyp(s, a); typ == nil && t != nil {
				typ, first = t, a
			}
		}
		if size == 0 {
			if typ == nil {
				return runtime.NIL, nil
			}
			return stdlib.NilSliceLike(first, typ), nil
		}
		out := make([]runtime.Value, 0, size)
		for _, p := range parts {
			for _, v := range p {
				out = append(out, runtime.Copy(v))
			}
		}
		return stdlib.RetagSlice(first, &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}
