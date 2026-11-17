package minigo

import (
	"fmt"
	"go/ast"
	"maps"
	"reflect"
	"slices"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installMaps(h *hostHelpers) {
	e.Bind("maps", genericFns(e.mapsSeqHelpers(map[string]runtime.Value{
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
			dst, _, _, okDst := scriptMapSeqArg(vc, args[0])
			src, _, _, okSrc := scriptMapSeqArg(vc, args[1])
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
		"EqualFunc":  mapsEqualFunc(),
		"DeleteFunc": mapsDeleteFunc(),
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

// mapsEqualFunc is maps.EqualFunc: equal sizes, every key of m1 present
// in m2, and eq(v1, v2) true for it. Keys are looked up by their script
// value (the two maps may key differently), so a NaN key is never found.
func mapsEqualFunc() *runtime.BuiltinFunc {
	const name = "maps.EqualFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 3 {
			return nil, fmt.Errorf("%s needs 3 args, got %d", name, len(args))
		}
		m1, _, _, ok1 := scriptMapSeqArg(vc, args[0])
		m2, _, _, ok2 := scriptMapSeqArg(vc, args[1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("%s: first two args must be maps", name)
		}
		// a nil map holds no pairs
		var n1, n2 int
		if m1 != nil {
			n1 = m1.Len()
		}
		if m2 != nil {
			n2 = m2.Len()
		}
		if n1 != n2 {
			return false, nil
		}
		if n1 == 0 {
			return true, nil
		}
		w := m1.BeginWalk()
		for i := range w.Len() {
			k, v1, ok := w.At(i)
			if !ok {
				continue
			}
			v2, ok := m2.Get(k)
			if !ok || !callBool(vc, args[2], v1, v2) {
				return false, nil
			}
		}
		return true, nil
	}}
}

// mapsDeleteFunc is maps.DeleteFunc: every entry for which del(k, v) is
// true is removed, the walk skipping entries del itself removed earlier.
func mapsDeleteFunc() *runtime.BuiltinFunc {
	const name = "maps.DeleteFunc"
	return &runtime.BuiltinFunc{Name: name, SyncCallbacks: true, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("%s needs 2 args, got %d", name, len(args))
		}
		m, kt, _, ok := scriptMapSeqArg(vc, args[0])
		if !ok {
			return nil, fmt.Errorf("%s: first arg must be a map", name)
		}
		if m == nil {
			return runtime.NIL, nil
		}
		conv := mapKeyConv(vc, m, kt)
		w := m.BeginWalk()
		for i := range w.Len() {
			k, v, ok := w.At(i)
			if !ok {
				continue
			}
			ak := k
			if conv != nil {
				ak = conv(k)
			}
			if callBool(vc, args[1], ak, v) {
				m.Delete(k)
			}
		}
		return runtime.NIL, nil
	}}
}

// mapKeyType resolves the declared key type of a map typedef — the
// typedef and its type expression; nil when td spells no map type.
func mapKeyType(vc runtime.ExprResolver, td *runtime.TypeDef) (*runtime.TypeDef, ast.Expr) {
	mt := mapTypeExpr(td)
	if mt == nil {
		return nil, nil
	}
	kt, err := vc.ResolveType(td, mt.Key)
	if err != nil {
		return nil, nil
	}
	return kt, mt.Key
}

// mapTypeExpr is the map type expression a map typedef spells — its
// own anonymous `map[K]V` or a named type's declaration; nil otherwise.
func mapTypeExpr(td *runtime.TypeDef) *ast.MapType {
	if td == nil {
		return nil
	}
	mt, _ := td.TypeExpr().(*ast.MapType)
	return mt
}

func mapKeys(v any) *runtime.Slice {
	if n, ok := v.(*runtime.Named); ok {
		return mapKeys(n.V)
	}
	if m, ok := v.(*runtime.Map); ok {
		return &runtime.Slice{Elems: append([]runtime.Value{}, m.Order...)}
	}
	if m, ok := v.(map[any]any); ok {
		return &runtime.Slice{Elems: slices.Collect(maps.Keys(m))}
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map {
		// a boxed host named map (http.Header, url.Values)
		el := make([]runtime.Value, 0, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			el = append(el, scriptVal(it.Key().Interface()))
		}
		return &runtime.Slice{Elems: el}
	}
	return &runtime.Slice{}
}

func mapValues(v any) *runtime.Slice {
	if n, ok := v.(*runtime.Named); ok {
		return mapValues(n.V)
	}
	if m, ok := v.(*runtime.Map); ok {
		el := make([]runtime.Value, m.Len())
		for i := range el {
			_, el[i] = m.At(i)
		}
		return &runtime.Slice{Elems: el}
	}
	if m, ok := v.(map[any]any); ok {
		return &runtime.Slice{Elems: slices.Collect(maps.Values(m))}
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map {
		// a boxed host named map (http.Header, url.Values)
		el := make([]runtime.Value, 0, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			el = append(el, scriptVal(it.Value().Interface()))
		}
		return &runtime.Slice{Elems: el}
	}
	return &runtime.Slice{}
}
