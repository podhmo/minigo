package inspect

import (
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// normSpecSymbol folds the predeclared aliases to their canonical
// spellings — gc treats byte/uint8, rune/int32 and any/interface{}
// as identical types.
func normSpecSymbol(sid runtime.SymbolID) runtime.SymbolID {
	if sid.PackagePath == BuiltinPackagePath {
		switch sid.Name {
		case "byte":
			sid.Name = "uint8"
		case "rune":
			sid.Name = "int32"
		case "any":
			sid.Name = "interface {}"
		}
	}
	return sid
}

// specShapeKind classifies a TypeExpr by the shape gc's type identity
// compares, so a host-backed node matches the AST node spelling the
// same type. Named and builtin types are leaves — their SymbolID
// decides; composite spellings compare children by position.
func specShapeKind(te *TypeExpr) string {
	if te.ht != nil {
		t := te.ht
		if t.Name() != "" {
			return "leaf"
		}
		switch t.Kind() {
		case reflect.Slice, reflect.Array:
			return "array"
		case reflect.Pointer:
			return "ptr"
		case reflect.Map:
			return "map"
		case reflect.Chan:
			return "chan"
		case reflect.Func:
			return "func"
		case reflect.Interface:
			if t.NumMethod() == 0 {
				return "leaf" // the empty interface spells a builtin
			}
			return "iface"
		case reflect.Struct:
			return "struct"
		}
		return "leaf"
	}
	switch te.expr.(type) {
	case *ast.ArrayType, *ast.Ellipsis:
		return "array"
	case *ast.StarExpr:
		return "ptr"
	case *ast.MapType:
		return "map"
	case *ast.ChanType:
		return "chan"
	case *ast.FuncType:
		return "func"
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "iface"
	}
	return "leaf"
}

// hostShapeSame compares a pair where at least one side is
// host-backed: leaves compare normalized SymbolIDs, composites
// compare shape and children. Anonymous struct and non-empty
// interface spellings under-resolve — reflect drops the field names
// and tags that identity needs, so they report no match.
func hostShapeSame(a, b *TypeExpr, res Resolver) bool {
	k := specShapeKind(a)
	if k != specShapeKind(b) {
		return false
	}
	if k == "leaf" {
		sa, oka := a.SymbolID()
		sb, okb := b.SymbolID()
		return oka && okb && normSpecSymbol(sa) == normSpecSymbol(sb)
	}
	if k == "struct" || k == "iface" {
		return false
	}
	if !specShapeExtra(a, b, res) {
		return false
	}
	ca, cb := a.Children(), b.Children()
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if !specSame(ca[i], cb[i], res) {
			return false
		}
	}
	return true
}

// specShapeExtra is sameShapeExtra across the host/AST boundary:
// array lengths and channel directions must still agree.
func specShapeExtra(a, b *TypeExpr, res Resolver) bool {
	la, oka := specArrayLen(a, res)
	lb, okb := specArrayLen(b, res)
	if oka || okb {
		return oka && okb && la == lb
	}
	da, oka := specChanDir(a)
	db, okb := specChanDir(b)
	if oka || okb {
		return oka && okb && da == db
	}
	return true
}

// specArrayLen reports a fixed array length: nil on the AST side or a
// non-array kind reports none (a slice carries no length).
func specArrayLen(te *TypeExpr, res Resolver) (int64, bool) {
	if te.ht != nil {
		if te.ht.Kind() == reflect.Array {
			return int64(te.ht.Len()), true
		}
		return 0, false
	}
	at, ok := te.expr.(*ast.ArrayType)
	if !ok || at.Len == nil {
		return 0, false
	}
	return lenConst(te.Sub(at.Len), res)
}

// specChanDir normalizes a channel direction across the boundary:
// bidirectional is the default and compares equally on both sides.
func specChanDir(te *TypeExpr) (int, bool) {
	if te.ht != nil {
		if te.ht.Kind() != reflect.Chan {
			return 0, false
		}
		switch te.ht.ChanDir() {
		case reflect.SendDir:
			return 1, true
		case reflect.RecvDir:
			return 2, true
		}
		return 0, true
	}
	ct, ok := te.expr.(*ast.ChanType)
	if !ok {
		return 0, false
	}
	switch ct.Dir {
	case ast.SEND:
		return 1, true
	case ast.RECV:
		return 2, true
	}
	return 0, true
}

// hostPromoDepth returns how many anonymous-field hops inside rt
// carried name's promotion: 0 when no anonymous field offers it (rt
// declares it), else 1 + the depth inside the carrying field's type.
// A name promoted through two fields of one struct is already
// ambiguous inside rt and absent from its method set, so at most one
// field can carry it. Reflect exposes no path separating a promoted
// method from a declared one, so a host struct both declaring name
// and embedding a carrier of it reads as promoted — the method then
// sits deeper than gc's, which under-resolves rather than
// misattributes.
func hostPromoDepth(rt reflect.Type, name string) int {
	st := rt
	for st.Kind() == reflect.Ptr {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return 0
	}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.Anonymous {
			continue
		}
		if _, ok := f.Type.MethodByName(name); !ok {
			fe := f.Type
			for fe.Kind() == reflect.Ptr {
				fe = fe.Elem()
			}
			if fe.Kind() == reflect.Interface {
				continue
			}
			if _, ok := reflect.PointerTo(fe).MethodByName(name); !ok {
				continue
			}
		}
		return 1 + hostPromoDepth(f.Type, name)
	}
	return 0
}
