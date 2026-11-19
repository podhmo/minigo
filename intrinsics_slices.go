package minigo

import (
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installSlices(h *hostHelpers) {
	e.Bind("slices", genericFns(hostSliceBoundary(e.slicesSeqHelpers(map[string]runtime.Value{
		"Sort": h.sortInPlace("slices.Sort"),
		"Contains": &runtime.BuiltinFunc{Name: "slices.Contains", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			i, err := sliceIndexScript(vc, "slices.Contains", args)
			return i >= 0, err
		}},
		"Index": &runtime.BuiltinFunc{Name: "slices.Index", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			i, err := sliceIndexScript(vc, "slices.Index", args)
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
				return &runtime.Slice{Elems: cloneElems(s.Elems), Typ: s.Typ}, nil
			case *runtime.Named:
				if _, ok := s.V.(*runtime.TypedNil); ok {
					return s, nil
				}
				if sl, ok := s.V.(*runtime.Slice); ok {
					return runtime.Tag(s.Typ, &runtime.Slice{Elems: cloneElems(sl.Elems), Typ: sl.Typ}), nil
				}
			}
			return nil, fmt.Errorf("slices.Clone: arg must be a slice")
		}},
		"Concat": slicesConcat(),
		"Equal": &runtime.BuiltinFunc{Name: "slices.Equal", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.Equal needs 2 args, got %d", len(args))
			}
			xs, xt, okx := scriptSliceArg(vc, args[0])
			ys, yt, oky := scriptSliceArg(vc, args[1])
			if !okx || !oky {
				return slices.Equal(anySlice(hostbridge.GoNative(args[0])), anySlice(hostbridge.GoNative(args[1]))), nil
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
		"IsSorted": h.fn("slices.IsSorted", func(a []any) (any, error) {
			el := scriptElems(a[0])
			return sort.SliceIsSorted(el, func(i, j int) bool { return lessScript(el[i], el[j]) }), nil
		}),
		"DeleteFunc": &runtime.BuiltinFunc{Name: "slices.DeleteFunc", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.DeleteFunc needs 2 args")
			}
			s, ok := sliceOf(args[0])
			if !ok {
				if nilish(args[0]) {
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
			return h.packSlice(vc, s, out), nil
		}},
		"Compact": &runtime.BuiltinFunc{Name: "slices.Compact", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("slices.Compact needs 1 arg")
			}
			s, ok := sliceOf(args[0])
			if !ok {
				if nilish(args[0]) {
					return args[0], nil
				}
				return nil, fmt.Errorf("slices.Compact: arg must be a slice")
			}
			_, et, _ := scriptSliceArg(vc, args[0])
			var out []runtime.Value
			for i, el := range s.Elems {
				if i > 0 && vc.Equal(et, out[len(out)-1], el) {
					continue
				}
				out = append(out, el)
			}
			return h.packSlice(vc, s, out), nil
		}},
		"Repeat": slicesRepeat(),
		"Grow": &runtime.BuiltinFunc{Name: "slices.Grow", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.Grow needs 2 args")
			}
			n := int64Of(args[1])
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
			return retagSlice(args[0], &runtime.Slice{Elems: grown[:len(elems)], Typ: td}), nil
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
					return retagSlice(args[0], &c), nil
				}
				return retagSlice(args[0], &runtime.Slice{Elems: s.Elems[:len(s.Elems):len(s.Elems)], Typ: s.Typ}), nil
			case *runtime.TypedNil:
				return args[0], nil
			}
			return nil, fmt.Errorf("slices.Clip: arg must be a slice")
		}},
		"SortFunc":         h.sortByCmpFunc("slices.SortFunc"),
		"SortStableFunc":   h.sortByCmpFunc("slices.SortStableFunc"),
		"BinarySearch":     slicesBinarySearch(false),
		"BinarySearchFunc": slicesBinarySearch(true),
		"EqualFunc":        slicesEqualFunc(),
		"IndexFunc":        slicesIndexFunc(),
		"ContainsFunc": &runtime.BuiltinFunc{Name: "slices.ContainsFunc", SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("slices.ContainsFunc needs 2 args, got %d", len(args))
			}
			s, err := sliceParam("slices.ContainsFunc", args[0])
			if err != nil {
				return nil, err
			}
			for _, el := range sliceElems(s) {
				if callBool(vc, args[1], el) {
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
		"CompactFunc":  h.slicesCompactFunc(),
		"Replace":      slicesReplace(),
		"Max": &runtime.BuiltinFunc{Name: "slices.Max", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return sliceExtreme(vc, "slices.Max", args, func(best, x runtime.Value) bool { return lessScript(best, x) })
		}},
		"Min": &runtime.BuiltinFunc{Name: "slices.Min", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return sliceExtreme(vc, "slices.Min", args, func(best, x runtime.Value) bool { return lessScript(x, best) })
		}},
		"Reverse": slicesReverse(),
		"Insert":  slicesInsert(),
		"Delete":  slicesDelete(),
	}), slicesHostUse, &firstSliceRead)))
}

// slicesCompare is slices.Compare (elements ordered with cmp.Compare)
// and slices.CompareFunc (elements ordered by the script callback): the
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
		s1, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		s2, err := sliceParam(name, args[1])
		if err != nil {
			return nil, err
		}
		x, y := sliceElems(s1), sliceElems(s2)
		for i, v1 := range x {
			if i >= len(y) {
				return int64(1), nil
			}
			c := int64(0)
			if withFunc {
				c = callInt(vc, args[2], v1, y[i])
			} else {
				c = cmpScript(v1, y[i])
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

// slicesIsSortedFunc is slices.IsSortedFunc: cmp(x[i], x[i-1]) < 0 for
// any i, probed from the end like Go does.
func slicesIsSortedFunc() *runtime.BuiltinFunc {
	const name = "slices.IsSortedFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		x := sliceElems(s)
		for i := len(x) - 1; i > 0; i-- {
			if callInt(vc, args[1], x[i], x[i-1]) < 0 {
				return false, nil
			}
		}
		return true, nil
	}}
}

// slicesExtremeFunc is slices.MaxFunc/MinFunc: the first element wins
// until a later one compares better against it (cmp(x[i], best) passes
// the better test). An empty slice panics like Go.
func slicesExtremeFunc(name string, better func(c int64) bool) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		x := sliceElems(s)
		if len(x) == 0 {
			panic(emptyListPanic(name))
		}
		best := x[0]
		for _, el := range x[1:] {
			if better(callInt(vc, args[1], el, best)) {
				best = el
			}
		}
		return best, nil
	}}
}

// slicesCompactFunc is slices.CompactFunc: an element is dropped when
// eq(x[i], x[i-1]) holds against the ORIGINAL neighbour, so a
// non-transitive eq behaves as it does in Go. The kept elements move to
// the front of the backing array and the vacated tail is zeroed, like
// slices.DeleteFunc here.
func (h *hostHelpers) slicesCompactFunc() *runtime.BuiltinFunc {
	const name = "slices.CompactFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		if s == nil || len(s.Elems) < 2 {
			return args[0], nil
		}
		kept := make([]runtime.Value, 1, len(s.Elems))
		kept[0] = s.Elems[0]
		for i := 1; i < len(s.Elems); i++ {
			if !callBool(vc, args[1], s.Elems[i], s.Elems[i-1]) {
				kept = append(kept, s.Elems[i])
			}
		}
		return retagSlice(args[0], h.packSlice(vc, s, kept)), nil
	}}
}

// slicesReplace is slices.Replace(s, i, j, v...): Go's own algorithm
// runs over the script slice's backing array (so spare capacity is
// reused and an overlapping v is handled as in Go), and the cleared
// tail is zeroed to the element type's zero value instead of a hole.
func slicesReplace() *runtime.BuiltinFunc {
	const name = "slices.Replace"
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < 3 {
			return nil, fmt.Errorf("%s(slice, i, j, elems...) needs at least 3 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		typ := sliceParamTyp(s, args[0])
		vs, err := elemOperands(vc, name, typ, args[3:])
		if err != nil {
			return nil, err
		}
		// a bounds failure panics in the host exactly as it would in Go
		// (the backing array has the script slice's len and cap); the
		// VM wraps it as the runtime.boundsError the script recovers.
		base := sliceElems(s)
		out := slices.Replace(base, int(intOf(args[1])), int(intOf(args[2])), vs...)
		if s == nil && len(out) == 0 {
			return args[0], nil // nothing to put in a nil slice: it stays nil
		}
		// a shrunk result shares the backing array; Go cleared the
		// vacated slots with a hole, the script's slot holds the zero.
		if len(out) < len(base) {
			fillHoles(vc, typ, base[len(out):])
		}
		return retagSlice(args[0], &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}

// slicesIndexFunc is slices.IndexFunc: the first index whose element
// satisfies the predicate, -1 for none or for a nil slice.
func slicesIndexFunc() *runtime.BuiltinFunc {
	const name = "slices.IndexFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		for i, el := range sliceElems(s) {
			if callBool(vc, args[1], el) {
				return int64(i), nil
			}
		}
		return int64(-1), nil
	}}
}

// slicesEqualFunc is slices.EqualFunc: equal lengths and eq true for
// every pair, probed in order. Either slice may be nil — a nil slice
// equals an empty one.
func slicesEqualFunc() *runtime.BuiltinFunc {
	const name = "slices.EqualFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("%s needs 3 args, got %d", name, len(args))
		}
		s1, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		s2, err := sliceParam(name, args[1])
		if err != nil {
			return nil, err
		}
		x, y := sliceElems(s1), sliceElems(s2)
		if len(x) != len(y) {
			return false, nil
		}
		for i := range x {
			if !callBool(vc, args[2], x[i], y[i]) {
				return false, nil
			}
		}
		return true, nil
	}}
}

// slicesBinarySearch is slices.BinarySearch (elements ordered with
// cmp.Compare) and slices.BinarySearchFunc (ordered by the script cmp
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
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		x, target := sliceElems(s), args[1]
		cmpAt := func(i int) int64 {
			if withFunc {
				return callInt(vc, args[2], x[i], target)
			}
			return cmpScript(x[i], target)
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

// slicesReverse is slices.Reverse: the elements swap in place; a nil
// slice, or one of zero-size elements (all alike), has nothing to move.
func slicesReverse() *runtime.BuiltinFunc {
	const name = "slices.Reverse"
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		s, err := sliceParamAny(name, args[0])
		if err != nil {
			return nil, err
		}
		if s != nil && !s.Virtual() {
			slices.Reverse(s.Elems)
		}
		return runtime.NIL, nil
	}}
}

// slicesInsert is slices.Insert(s, i, v...): Go's own algorithm runs
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
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		typ := sliceParamTyp(s, args[0])
		vs, err := elemOperands(vc, name, typ, args[2:])
		if err != nil {
			return nil, err
		}
		base := sliceElems(s)
		out := slices.Insert(base, intOf(args[1]), vs...)
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
		fillHoles(vc, typ, out[len(out):cap(out)])
		return retagSlice(args[0], &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}

// slicesDelete is slices.Delete(s, i, j): the elements move down over
// [i, j) in the backing array and the vacated tail is zeroed, so slices
// re-sliced from the original observe the deletion. A nil slice stays
// nil; a bad range panics with the boundsError the script recovers.
func slicesDelete() *runtime.BuiltinFunc {
	const name = "slices.Delete"
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("%s(slice, i, j) needs 3 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		base := sliceElems(s)
		out := slices.Delete(base, intOf(args[1]), intOf(args[2]))
		if s == nil {
			return args[0], nil // only the empty range [0, 0) passed the bounds check
		}
		typ := sliceParamTyp(s, args[0])
		if len(out) < len(base) {
			fillHoles(vc, typ, base[len(out):])
		}
		return retagSlice(args[0], &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}

// slicesRepeat is slices.Repeat(s, n): a new slice (never nil, even for
// a nil s or n == 0) of n copies of s, sized exactly len(s)*n.
func slicesRepeat() *runtime.BuiltinFunc {
	const name = "slices.Repeat"
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		s, err := sliceParam(name, args[0])
		if err != nil {
			return nil, err
		}
		n := intOf(args[1])
		if n < 0 {
			panic(&runtime.Panic{Value: "cannot be negative"})
		}
		el := sliceElems(s)
		if len(el) > 0 && n > math.MaxInt/len(el) {
			panic(&runtime.Panic{Value: "the result of (len(x) * count) overflows"})
		}
		out := make([]runtime.Value, 0, len(el)*n)
		for range n {
			for _, v := range el {
				out = append(out, runtime.Copy(v))
			}
		}
		return retagSlice(args[0], &runtime.Slice{Elems: out, Typ: sliceParamTyp(s, args[0])}), nil
	}}
}

// slicesConcat is slices.Concat: the arguments' elements in one new
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
			s, err := sliceParam(name, a)
			if err != nil {
				return nil, err
			}
			parts[i] = sliceElems(s)
			size += len(parts[i])
			if t := sliceParamTyp(s, a); typ == nil && t != nil {
				typ, first = t, a
			}
		}
		if size == 0 {
			if typ == nil {
				return runtime.NIL, nil
			}
			return nilSliceLike(first, typ), nil
		}
		out := make([]runtime.Value, 0, size)
		for _, p := range parts {
			for _, v := range p {
				out = append(out, runtime.Copy(v))
			}
		}
		return retagSlice(first, &runtime.Slice{Elems: out, Typ: typ}), nil
	}}
}
