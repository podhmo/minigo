package sort

import (
	"fmt"
	gsort "sort"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func Install(bind stdlib.Bind, h stdlib.Helpers) {
	bind("sort", stdlib.HostSliceBoundary(map[string]runtime.Value{
		"Ints":     stdlib.SortInPlace("sort.Ints"),
		"Float64s": stdlib.SortInPlace("sort.Float64s"),
		"Strings":  stdlib.SortInPlace("sort.Strings"),
		// gsort.Sort adapts a script object with Len/Less/Swap methods to
		// a host gsort.Interface (a GoValue gsort.Interface passes through).
		"Sort":  &runtime.BuiltinFunc{Name: "sort.Sort", Fn: stdlib.SortInterface},
		"Slice": &runtime.BuiltinFunc{Name: "sort.Slice", Fn: stdlib.SortSlice},
		// the typed search/sorted wrappers marshal through WrapBind like
		// any other bound func.
		"SearchInts":        stdlib.WrapBind("sort.SearchInts", gsort.SearchInts),
		"SearchFloat64s":    stdlib.WrapBind("sort.SearchFloat64s", gsort.SearchFloat64s),
		"SearchStrings":     stdlib.WrapBind("sort.SearchStrings", gsort.SearchStrings),
		"IntsAreSorted":     stdlib.WrapBind("sort.IntsAreSorted", gsort.IntsAreSorted),
		"Float64sAreSorted": stdlib.WrapBind("sort.Float64sAreSorted", gsort.Float64sAreSorted),
		"StringsAreSorted":  stdlib.WrapBind("sort.StringsAreSorted", gsort.StringsAreSorted),
		"SliceIsSorted": &runtime.BuiltinFunc{Name: "sort.SliceIsSorted", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if _, rv, isHost := vm.HostSliceOf(args[0]); isHost {
				// a boxed host slice: the less function reads it live
				for i := rv.Len() - 1; i > 0; i-- {
					r := hostbridge.CallOrPanic(v, args[1], []runtime.Value{int64(i), int64(i - 1)})
					if b, _ := r.(bool); b {
						return false, nil
					}
				}
				return true, nil
			}
			s, ok := stdlib.SliceOf(args[0])
			if !ok {
				if stdlib.NilSliceArg(args[0]) {
					return true, nil // a nil slice is sorted
				}
				return nil, fmt.Errorf("sort.SliceIsSorted: first arg must be a slice")
			}
			less := args[1]
			for i := len(s.Elems) - 1; i > 0; i-- {
				r := hostbridge.CallOrPanic(v, less, []runtime.Value{int64(i), int64(i - 1)})
				if b, _ := r.(bool); b {
					return false, nil
				}
			}
			return true, nil
		}},
		"Search": &runtime.BuiltinFunc{Name: "sort.Search", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			n := stdlib.Int64Of(hostbridge.GoNative(args[0]))
			f := args[1]
			i, j := int64(0), n
			for i < j {
				m := int64(uint64(i+j) >> 1)
				r := hostbridge.CallOrPanic(v, f, []runtime.Value{m})
				if b, _ := r.(bool); b {
					j = m
				} else {
					i = m + 1
				}
			}
			return i, nil
		}},
		"SliceStable": &runtime.BuiltinFunc{Name: "sort.SliceStable", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if stdlib.HostSortSlice(v, args[0], args[1], true) {
				return runtime.NIL, nil // a boxed host slice (net.IP)
			}
			s, ok := stdlib.SliceOf(args[0])
			if !ok {
				if stdlib.NilSliceArg(args[0]) {
					return runtime.NIL, nil // sorting a nil slice is a no-op in Go
				}
				return nil, fmt.Errorf("sort.SliceStable: first arg must be a slice")
			}
			less := args[1]
			gsort.SliceStable(s.Elems, func(i, j int) bool {
				r := hostbridge.CallOrPanic(v, less, []runtime.Value{int64(i), int64(j)})
				b, _ := r.(bool)
				return b
			})
			return runtime.NIL, nil
		}},
	}, stdlib.SortHostUse, nil))
}
