package maps

import (
	"fmt"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, typedSeq func(*runtime.BuiltinFunc, ...*runtime.TypeDef) runtime.Value) {
	bind("maps", stdlib.GenericFns(stdlib.MapsSeqHelpers(typedSeq, map[string]runtime.Value{
		"Clone": &runtime.BuiltinFunc{Name: "maps.Clone", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("maps.Clone needs 1 arg")
			}
			// Clone keeps the declared map type: without Typ a missing key
			// reads NIL and `c[k]++` traps instead of zero-starting.
			clone := (*runtime.Map).Clone
			switch m := args[0].(type) {
			case *runtime.TypedNil:
				// Clone(nil) is the same typed nil map.
				return m, nil
			case *runtime.Map:
				return clone(m), nil
			case *runtime.Named:
				if _, ok := m.V.(*runtime.TypedNil); ok {
					return m, nil
				}
				if mm, ok := m.V.(*runtime.Map); ok {
					return runtime.Tag(m.Typ, clone(mm)), nil
				}
			}
			return nil, fmt.Errorf("maps.Clone: arg must be a map")
		}},
		"Copy": &runtime.BuiltinFunc{Name: "maps.Copy", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("maps.Copy needs 2 args, got %d", len(args))
			}
			dst, _, _, okDst := stdlib.ScriptMapSeqArg(vc, args[0])
			src, _, _, okSrc := stdlib.ScriptMapSeqArg(vc, args[1])
			if !okDst || !okSrc {
				return nil, fmt.Errorf("maps.Copy: args must be maps")
			}
			// a nil src copies nothing; a nil dst only fails when
			// there is an entry to store.
			if src == nil || src.Len() == 0 {
				return runtime.NIL, nil
			}
			if dst == nil {
				panic(runtime.NilMapAssignPanic())
			}
			for i := 0; i < src.Len(); i++ {
				k, e := src.At(i)
				dst.Insert(k, e)
			}
			return runtime.NIL, nil
		}},
		"EqualFunc":  stdlib.MapsEqualFunc(),
		"DeleteFunc": stdlib.MapsDeleteFunc(),
		"Equal": &runtime.BuiltinFunc{Name: "maps.Equal", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("maps.Equal needs 2 args, got %d", len(args))
			}
			// a nil map (or anything else that is no script map) holds
			// no pairs: maps.Equal(nilMap, map[K]V{}) is true.
			a, _ := runtime.Unwrap(args[0]).(*runtime.Map)
			b, _ := runtime.Unwrap(args[1]).(*runtime.Map)
			if a == nil || b == nil {
				return (a == nil || a.Len() == 0) && (b == nil || b.Len() == 0), nil
			}
			if a.Len() != b.Len() {
				return false, nil
			}
			// values compare with Go's == at the declared value type: a
			// host FileMode (os.Stat) meets fs.ModeDir, an interface
			// value compares its dynamic type too.
			var et *runtime.TypeDef
			if a.Typ != nil {
				et, _ = vc.ElemOf(a.Typ)
			}
			// look each key up by its script value: the two maps may key
			// differently (an interface-keyed map made in the script vs
			// one built host-side), and a NaN key is never found, as in Go.
			for i := 0; i < a.Len(); i++ {
				k, av := a.At(i)
				bv, ok := b.Get(k)
				if !ok || !vc.Equal(et, av, bv) {
					return false, nil
				}
			}
			return true, nil
		}},
	})))
}
