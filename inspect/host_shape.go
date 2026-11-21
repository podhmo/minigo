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
// array lengths, func parameter counts and channel directions must
// still agree — the children compare alone would merge
// func(any, any, bool) with func(any, any) bool.
func specShapeExtra(a, b *TypeExpr, res Resolver) bool {
	la, oka := specArrayLen(a, res)
	lb, okb := specArrayLen(b, res)
	if oka != okb || la != lb {
		return false
	}
	na, oka := specFuncIns(a)
	nb, okb := specFuncIns(b)
	if oka != okb || na != nb {
		return false
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
	if n, ok := lenConst(te.Sub(at.Len), res); ok {
		return n, true
	}
	// a fixed-length spelling whose length does not const-evaluate
	// still is an array — reporting none would match it against a
	// slice, so -1 mismatches every host-side length instead.
	return -1, true
}

// specFuncIns reports a func spelling's parameter count — its
// children list merges inputs and results, so the boundary needs a
// separate check.
func specFuncIns(te *TypeExpr) (int, bool) {
	if te.ht != nil {
		if te.ht.Kind() == reflect.Func {
			return te.ht.NumIn(), true
		}
		return 0, false
	}
	ft, ok := te.expr.(*ast.FuncType)
	if !ok {
		return 0, false
	}
	n := 0
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			if len(f.Names) == 0 {
				n++
			} else {
				n += len(f.Names)
			}
		}
	}
	return n, true
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
	return hostPromoDepthSeen(rt, name, map[reflect.Type]bool{})
}

// hostPromoDepthSeen walks the anonymous-field chain carrying name;
// seen guards the descent — declared Go types cannot cycle their
// embeds, but a reflect.StructOf-built or otherwise synthetic type
// could, and a cycle would otherwise recurse without bound.
func hostPromoDepthSeen(rt reflect.Type, name string, seen map[reflect.Type]bool) int {
	st := rt
	for st.Kind() == reflect.Ptr {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct || seen[st] {
		return 0
	}
	seen[st] = true
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
		return 1 + hostPromoDepthSeen(f.Type, name, seen)
	}
	return 0
}
