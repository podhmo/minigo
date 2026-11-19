package minigo

import (
	"cmp"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installSort(h *hostHelpers) {
	e.Bind("sort", hostSliceBoundary(map[string]runtime.Value{
		"Ints":     h.sortInPlace("sort.Ints"),
		"Float64s": h.sortInPlace("sort.Float64s"),
		"Strings":  h.sortInPlace("sort.Strings"),
		// sort.Sort adapts a script object with Len/Less/Swap methods to
		// a host sort.Interface (a GoValue sort.Interface passes through).
		"Sort":  &runtime.BuiltinFunc{Name: "sort.Sort", Fn: h.sortInterface},
		"Slice": &runtime.BuiltinFunc{Name: "sort.Slice", Fn: h.sortSlice},
		"SliceIsSorted": &runtime.BuiltinFunc{Name: "sort.SliceIsSorted", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if _, rv, isHost := vm.HostSliceOf(args[0]); isHost {
				// a boxed host slice: the less function reads it live
				for i := rv.Len() - 1; i > 0; i-- {
					r := callOrPanic(v, args[1], []runtime.Value{int64(i), int64(i - 1)})
					if b, _ := r.(bool); b {
						return false, nil
					}
				}
				return true, nil
			}
			s, ok := sliceOf(args[0])
			if !ok {
				if nilSliceArg(args[0]) {
					return true, nil // a nil slice is sorted
				}
				return nil, fmt.Errorf("sort.SliceIsSorted: first arg must be a slice")
			}
			less := args[1]
			for i := len(s.Elems) - 1; i > 0; i-- {
				r := callOrPanic(v, less, []runtime.Value{int64(i), int64(i - 1)})
				if b, _ := r.(bool); b {
					return false, nil
				}
			}
			return true, nil
		}},
		"Search": &runtime.BuiltinFunc{Name: "sort.Search", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n := int64Of(hostbridge.GoNative(args[0]))
			f := args[1]
			i, j := int64(0), n
			for i < j {
				m := int64(uint64(i+j) >> 1)
				r := callOrPanic(v, f, []runtime.Value{m})
				if b, _ := r.(bool); b {
					j = m
				} else {
					i = m + 1
				}
			}
			return i, nil
		}},
		"SliceStable": &runtime.BuiltinFunc{Name: "sort.SliceStable", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if hostSortSlice(v, args[0], args[1], true) {
				return runtime.NIL, nil // a boxed host slice (net.IP)
			}
			s, ok := sliceOf(args[0])
			if !ok {
				if nilSliceArg(args[0]) {
					return runtime.NIL, nil // sorting a nil slice is a no-op in Go
				}
				return nil, fmt.Errorf("sort.SliceStable: first arg must be a slice")
			}
			less := args[1]
			sort.SliceStable(s.Elems, func(i, j int) bool {
				r := callOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
				b, _ := r.(bool)
				return b
			})
			return runtime.NIL, nil
		}},
	}, sortHostUse, nil))
}

// sortScript orders int64/float64/string elements ascending — the element
// sets sort.Ints, sort.Strings, and slices.Sort support.
func sortScript(el []runtime.Value) {
	sort.Slice(el, func(i, j int) bool {
		bv := orderScalar(el[j])
		switch a := orderScalar(el[i]).(type) {
		case int64:
			if b, ok := bv.(int64); ok {
				return a < b
			}
		case float64:
			if b, ok := bv.(float64); ok {
				// sort.Float64s orders NaN before all other values
				return a < b || (math.IsNaN(a) && !math.IsNaN(b))
			}
		case string:
			if b, ok := bv.(string); ok {
				return a < b
			}
		}
		return false
	})
}

// sortSlice implements sort.Slice: the less function is a script callable.
// scriptElems exposes a slice argument's raw elements to Go-side helpers
// (the argument may arrive as a []any via hostbridge.GoNative).
func scriptElems(v any) []runtime.Value {
	switch s := v.(type) {
	case *runtime.Slice:
		return s.Elems
	case []any:
		el := make([]runtime.Value, len(s))
		for i, x := range s {
			el[i] = scriptVal(x)
		}
		return el
	}
	return nil
}

// lessScript orders int64/float64/string/bool (heterogeneous pairs rank by kind:
// numbers < strings < others, comparing numerically across int64/float64).
func lessScript(a, b runtime.Value) bool {
	a = orderScalar(a)
	b = orderScalar(b)
	an, aok := numOf(a)
	bn, bok := numOf(b)
	if aok && bok {
		// NaN orders before all other values, like cmp.Compare —
		// slices.Sort/IsSorted/Search share this ordering.
		return an < bn || (math.IsNaN(an) && !math.IsNaN(bn))
	}
	if ab, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			// false sorts before true, like fmtsort — never by a
			// named bool's String text.
			return !ab && bb
		}
	}
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return as < bs
		}
		return false // strings rank above numbers
	}
	if aok {
		return true
	}
	return false
}

// orderScalar is the scalar the ordering helpers compare for v: tags
// peel, and a host named basic — boxed (a FileMode from os.Stat, a
// token.Token off inspect.AST) or raw (a time.Month or reflect.Kind a
// host call returned) — reads as its payload, so it orders among the
// source values of its type.
func orderScalar(v runtime.Value) runtime.Value {
	v = runtime.Unwrap(v)
	switch x := v.(type) {
	case int64, float64, string, bool, time.Duration, *runtime.UConst, runtime.Nil:
		return v
	case *runtime.GoValue:
		if p, _, ok := runtime.HostNamedScalar(x); ok {
			return p
		}
		return v
	}
	if p, _, ok := runtime.HostNamedPayload(v); ok {
		return p
	}
	return v
}

// scriptSortable is the sort.Interface view of a script object.
type scriptSortable struct {
	vc                    runtime.ScriptCaller
	lenFn, lessFn, swapFn runtime.Value
}

// cmpScript is cmp.Compare over script values, with the ordering
// slices.Sort uses ([lessScript]: NaN before every other float, a host
// named basic ordered among its source twins). Two int64 payloads
// compare exactly — the float64 route would merge neighbours past 2^53.
func cmpScript(a, b runtime.Value) int64 {
	a, b = orderScalar(a), orderScalar(b)
	if x, ok := a.(int64); ok {
		if y, ok := b.(int64); ok {
			return int64(cmp.Compare(x, y))
		}
	}
	switch {
	case lessScript(a, b):
		return -1
	case lessScript(b, a):
		return 1
	}
	return 0
}

func (s *scriptSortable) Len() int {
	r, err := s.vc.Call(s.lenFn, nil)
	if err != nil {
		panic(err)
	}
	return int(int64Of(r))
}

func (s *scriptSortable) Less(i, j int) bool {
	r, err := s.vc.Call(s.lessFn, []runtime.Value{int64(i), int64(j)})
	if err != nil {
		panic(err)
	}
	b, _ := r.(bool)
	return b
}

func (s *scriptSortable) Swap(i, j int) {
	if _, err := s.vc.Call(s.swapFn, []runtime.Value{int64(i), int64(j)}); err != nil {
		panic(err)
	}
}
