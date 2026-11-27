package fixture

import (
	"fmt"
	"go/ast"
	"math"

	"github.com/podhmo/minigo/runtime"
)

// ImportPath is the package path stories import. It is a real Go package
// for native test/build use and a host-bound package while interpreted.
const ImportPath = "github.com/podhmo/minigo/x/storybook/fixture"

// Symbols returns the script-aware generic builtins bound under ImportPath.
func Symbols() map[string]runtime.Value {
	return map[string]runtime.Value{
		"Empty":    genericFixture("fixture.Empty", modeEmpty),
		"Minimal":  genericFixture("fixture.Minimal", modeMinimal),
		"Maximal":  genericFixture("fixture.Maximal", modeMaximal),
		"LongText": genericFixture("fixture.LongText", modeLongText),
	}
}

func genericFixture(name string, m mode) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, GenFn: func(vc runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
		if len(targs) != 1 {
			return nil, fmt.Errorf("%s needs 1 type argument", name)
		}
		if len(args) != 0 {
			return nil, fmt.Errorf("%s needs no arguments", name)
		}
		td, ok := targs[0].(*runtime.TypeDef)
		if !ok || td == nil {
			return nil, fmt.Errorf("%s: type argument is %T, not a type", name, targs[0])
		}
		return scriptValue(vc, td, m, 0, map[*runtime.TypeDef]bool{}), nil
	}}
}

func scriptValue(vc runtime.VMCaller, td *runtime.TypeDef, m mode, depth int, seen map[*runtime.TypeDef]bool) runtime.Value {
	if td == nil || depth > 5 || seen[td] || m == modeEmpty {
		return vc.Zero(td)
	}
	seen[td] = true
	defer delete(seen, td)

	u := td
	if x, err := vc.Underlying(td); err == nil && x != nil {
		u = x
	}
	switch u.Kind {
	case runtime.KindStruct:
		z := vc.Zero(td)
		st, ok := z.(*runtime.Struct)
		if !ok {
			return z
		}
		fts, err := vc.FieldTypes(td)
		if err != nil {
			return z
		}
		for i := range st.Fields {
			if i < len(fts) && fts[i] != nil {
				st.Fields[i] = convertOr(vc, fts[i], scriptValue(vc, fts[i], m, depth+1, seen))
			}
		}
		return st
	case runtime.KindNamedBasic:
		return convertOr(vc, td, scalarValue(u, m))
	case runtime.KindPointer:
		et, err := vc.ElemOf(td)
		if err != nil || et == nil {
			return vc.Zero(td)
		}
		return convertOr(vc, td, &runtime.Cell{Elem: scriptValue(vc, et, m, depth+1, seen)})
	case runtime.KindSlice:
		et, err := vc.ElemOf(td)
		if err != nil || et == nil {
			return vc.Zero(td)
		}
		n := scriptCount(vc, td, m)
		el := make([]runtime.Value, n)
		for i := range el {
			el[i] = convertOr(vc, et, scriptValue(vc, et, m, depth+1, seen))
		}
		return &runtime.Slice{Elems: el, Typ: td}
	case runtime.KindMap:
		return scriptMap(vc, td, u, m, depth, seen)
	default:
		return vc.Zero(td)
	}
}

func scalarValue(td *runtime.TypeDef, m mode) runtime.Value {
	name := runtime.BasicNameOf(td)
	if name == "" {
		name = td.Name
	}
	switch name {
	case "string":
		return textFor(m)
	case "bool":
		return true
	case "float32":
		if m == modeMaximal {
			return float64(math.MaxFloat32)
		}
		return float64(1.25)
	case "float64":
		if m == modeMaximal {
			return float64(math.MaxFloat64)
		}
		return float64(1.25)
	case "uint", "uint64", "uintptr":
		if m == modeMaximal {
			// script integers are int64-shaped; use the largest
			// representable positive value rather than a negative wrap.
			return int64(math.MaxInt64)
		}
		return int64(1)
	case "uint8", "byte":
		return int64Or(m, math.MaxUint8)
	case "uint16":
		return int64Or(m, math.MaxUint16)
	case "uint32":
		return int64Or(m, math.MaxUint32)
	case "int", "int64":
		if m == modeMaximal {
			return int64(math.MaxInt64)
		}
		return int64(1)
	case "int8":
		return int64Or(m, math.MaxInt8)
	case "int16":
		return int64Or(m, math.MaxInt16)
	case "int32", "rune":
		return int64Or(m, math.MaxInt32)
	default:
		return runtime.NIL
	}
}

func int64Or(m mode, max int64) int64 {
	if m == modeMaximal {
		return max
	}
	return 1
}

func scriptCount(vc runtime.VMCaller, td *runtime.TypeDef, m mode) int {
	if n, ok := vc.ArrayLenOf(td); ok {
		return int(n)
	}
	if m == modeMaximal {
		return 3
	}
	return 1
}

func scriptMap(vc runtime.VMCaller, td, u *runtime.TypeDef, m mode, depth int, seen map[*runtime.TypeDef]bool) runtime.Value {
	vt, err := vc.ElemOf(td)
	if err != nil || vt == nil {
		return vc.Zero(td)
	}
	kt := mapKeyType(vc, td, u)
	if kt == nil {
		return vc.Zero(td)
	}
	n := 1
	if m == modeMaximal {
		n = 3
	}
	out := vc.NewMap(td)
	for i := range n {
		k := scriptValue(vc, kt, m, depth+1, seen)
		k = varyScriptKey(k, i)
		v := scriptValue(vc, vt, m, depth+1, seen)
		out.Insert(convertOr(vc, kt, k), convertOr(vc, vt, v))
	}
	return out
}

func mapKeyType(vc runtime.VMCaller, td, u *runtime.TypeDef) *runtime.TypeDef {
	var x ast.Expr
	if mt, ok := u.Anon.(*ast.MapType); ok {
		x = mt.Key
	} else if mt, ok := td.Anon.(*ast.MapType); ok {
		x = mt.Key
	}
	if x == nil {
		return nil
	}
	kt, err := vc.ResolveType(td, x)
	if err != nil {
		return nil
	}
	return kt
}

func varyScriptKey(v runtime.Value, i int) runtime.Value {
	if i == 0 {
		return v
	}
	switch x := v.(type) {
	case string:
		return x + string(rune('a'+i))
	case int64:
		return x + int64(i)
	case *runtime.Named:
		return &runtime.Named{Typ: x.Typ, V: varyScriptKey(x.V, i)}
	default:
		return v
	}
}

func convertOr(vc runtime.VMCaller, td *runtime.TypeDef, v runtime.Value) runtime.Value {
	if td == nil {
		return v
	}
	if out, err := vc.Convert(td, v); err == nil {
		return out
	}
	return v
}
