// Typedef-shape helpers shared by the fmt and encoding/json installers —
// moved here so both root packages stay thin (P2-12).
package stdlib

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

// byteSliceTyp reports whether td's element type is byte/uint8 — the one
// slice fmt renders as text rather than a bracketed list.
func ByteSliceTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if td.Elem != nil {
		return TypedefSpelling(td.Elem) == "uint8"
	}
	at, ok := td.TypeExpr().(*ast.ArrayType)
	if !ok {
		return false
	}
	id, ok := at.Elt.(*ast.Ident)
	return ok && (id.Name == "byte" || id.Name == "uint8")
}

// elemTyp wraps a composite element's type AST so leaf rendering can
// spell bad-verb markers with the declared element type. Only idents
// naming a declared type carry the package — builtin type names spell
// unqualified like Go.
func ElemTyp(e ast.Expr, pkg *runtime.Package) *runtime.TypeDef {
	if e == nil {
		return nil
	}
	if id, ok := e.(*ast.Ident); ok {
		td := &runtime.TypeDef{Name: id.Name, Kind: runtime.KindNamedBasic}
		if pkg != nil && pkg.Index != nil {
			if itd, ok := pkg.Index.Types[id.Name]; ok && itd.Decl != nil {
				// an alias name never spells — the declared slot's
				// type is the target's (`type A = B` IS B).
				if ts, ok := itd.Decl.Spec.(*ast.TypeSpec); ok && ts.Assign.IsValid() {
					return ElemTyp(ts.Type, pkg)
				}
				td.Pkg = pkg
			}
		}
		return td
	}
	return &runtime.TypeDef{Pkg: pkg, Anon: e}
}

// sliceElemTyp resolves a slice/array typedef's element typedef.
func SliceElemTyp(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	if td.Elem != nil {
		return td.Elem
	}
	if at, ok := td.TypeExpr().(*ast.ArrayType); ok {
		return BindTyp(td, ElemTyp(at.Elt, td.Pkg))
	}
	return nil
}

// mapElemTyps resolves a map typedef's key and value typedefs.
func MapElemTyps(td *runtime.TypeDef) (key, val *runtime.TypeDef) {
	if mt, ok := td.TypeExpr().(*ast.MapType); ok && td != nil {
		return BindTyp(td, ElemTyp(mt.Key, td.Pkg)), BindTyp(td, ElemTyp(mt.Value, td.Pkg))
	}
	return nil, nil
}

// fieldTypOf resolves the i'th field's typedef from a struct typedef's
// declaration — embedded fields count once and multi-name decls expand.
func FieldTypOf(td *runtime.TypeDef, i int) *runtime.TypeDef {
	st, ok := td.TypeExpr().(*ast.StructType)
	if !ok {
		return nil
	}
	n := 0
	for _, fd := range st.Fields.List {
		cnt := len(fd.Names)
		if cnt == 0 {
			cnt = 1
		}
		if i < n+cnt {
			return BindTyp(td, ElemTyp(fd.Type, td.Pkg))
		}
		n += cnt
	}
	return nil
}

// bindTyp resolves a declared element typedef through the enclosing
// typedef's instantiation binds — a `Pair[error, any]` `Key K` field's
// declared type is `K`, but a value stored there statically has the
// bound argument's type. Composite field types keep the binds so a
// nested type-parameter name (a `[]K` field's element) resolves the
// same way one level down.
func BindTyp(td *runtime.TypeDef, et *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || et == nil || len(td.Binds) == 0 {
		return et
	}
	if et.Anon == nil && et.Spec == nil {
		if bv, ok := td.Binds[et.Name]; ok {
			if btd, ok := bv.(*runtime.TypeDef); ok {
				return btd
			}
		}
	}
	if len(et.Binds) == 0 {
		cp := et.CloneUncached()
		cp.Binds = td.Binds
		return cp
	}
	return et
}

// typedefSpelling renders a typedef for %T/#v output — it delegates to
// the canonical display speller.
func TypedefSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "interface{}"
	}
	return runtime.DisplayName(td)
}

// nilIfaceTyp reports the interface typedef when x is a nil interface
// value — an iface-kind IfaceNil, possibly Named-wrapped — and nil for
// any other value. A nil interface has no dynamic type: %T spells <nil>
// at top level, while under a pointer the declared typedef still shows.
func NilIfaceTyp(x runtime.Value) *runtime.TypeDef {
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	return runtime.IfaceTaggedNil(x)
}

// peelNil unwraps cells, refs and named wrappers down to a nil
// payload and reports its tagged typedef — the one peel-to-nil walk
// json's nil probe (any nilish payload) and fmt's pointer skip
// (pointer-tagged nil only) share.
func PeelNil(v runtime.Value) (*runtime.TypeDef, bool) {
	var seen map[runtime.Value]bool
	for {
		if dv, ok := runtime.Deref(v); ok {
			if seen == nil {
				seen = map[runtime.Value]bool{v: true}
			} else if seen[v] {
				// a cyclic pointer (x := any(nil); x = &x) is no nil
				return nil, false
			} else {
				seen[v] = true
			}
			v = dv
			continue
		}
		switch n := v.(type) {
		case *runtime.TypedNil:
			return n.Typ, true
		case *runtime.IfaceNil:
			return n.Typ, true // a TypedNil boxed by an interface slot keeps its tag
		}
		return nil, false
	}
}

// refDeclTyp recovers the declared type an &-operand points at when it
// arrives as a tracked ref: a FieldRef reads its field's declared
// typedef (an interface field spells *main.I of its stored concrete),
// an IndexRef its container's declared element typedef. A plain &var
// cell already stamps the same type on itself (cell.Typ), so refs
// share that answer. Nil when the ref kind or declared type is unknown.
func RefDeclTyp(v runtime.Value) *runtime.TypeDef {
	switch r := v.(type) {
	case *runtime.FieldRef:
		if st, idx, ok := r.Find(); ok && st != nil && st.Def != nil {
			return FieldTypOf(st.Def, idx)
		}
	case *runtime.IndexRef:
		if s := r.Slice(); s != nil {
			return SliceElemTyp(s.Typ)
		}
		if m := r.Map(); m != nil {
			_, et := MapElemTyps(m.Typ)
			return et
		}
	}
	return nil
}

// pointeeDeclTyp is the declared type an &-operand points at: a tracked
// ref answers through its declared field/element typedef (refDeclTyp),
// a plain &var cell through its own stamped Typ. Nil when the pointee's
// declared type is unknown.
func PointeeDeclTyp(v runtime.Value) *runtime.TypeDef {
	if td := RefDeclTyp(v); td != nil {
		return td
	}
	if c := AsCell(v); c != nil {
		return c.Typ
	}
	return nil
}

// AsCell peels named wrappers down to the underlying cell, or nil.
func AsCell(v runtime.Value) *runtime.Cell {
	for {
		switch x := v.(type) {
		case *runtime.Cell:
			return x
		case *runtime.Named:
			v = x.V
		default:
			return nil
		}
	}
}
