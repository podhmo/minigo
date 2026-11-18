package runtime

import (
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/syntax"
)

// ConvertibleTo reports whether a value of typedef a can be converted to
// typedef b under Go's conversion rules — the judgment behind `T(x)` and
// reflect's ConvertibleTo: identical underlying types (with struct tags
// ignored, unlike assignment), integer/floating numerics between
// themselves, complex only to complex, integer to string, string to and
// from []byte/[]rune, and a slice to an array or array pointer of the
// same element type. Whatever res cannot resolve answers false — the
// judgment is never wider than the rules allow.
func ConvertibleTo(a, b *TypeDef, res TypeResolver) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b || TypIdentical(a, b) {
		return true
	}
	r := orResolver(res)
	if NewShapeEq(r).Conv(a, b) {
		return true
	}
	ua, ub := r.Underlying(a), r.Underlying(b)
	if ua == nil || ub == nil {
		return false
	}
	// tag-blind conversion (Go 1.8+): identical underlying shapes
	// ignoring struct tags convert — unlike assignment. The peeled
	// shapes compare as anonymous shells: `S2(S1{v})` converts when
	// the literals behind S1 and S2 match modulo tags.
	if convTagBlind(anonShell(ua), anonShell(ub), r) {
		return true
	}
	na, nb := convBasicName(ua), convBasicName(ub)
	cls := func(n string) byte {
		switch n {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
			return 'i'
		case "float32", "float64":
			return 'f'
		case "complex64", "complex128":
			return 'c'
		case "string":
			return 's'
		}
		return 0
	}
	ca, cb := cls(na), cls(nb)
	if ca == 'c' || cb == 'c' {
		return ca == 'c' && cb == 'c'
	}
	num := func(c byte) bool { return c == 'i' || c == 'f' }
	if num(ca) && num(cb) {
		return true
	}
	if ca == 'i' && cb == 's' {
		return true // integer -> one-rune string
	}
	if ca == 's' && runeSliceTd(ub, r) {
		return true // string -> []byte / []rune
	}
	if cb == 's' && runeSliceTd(ua, r) {
		return true // []byte / []rune -> string
	}
	if convIsSlice(ua) && convIsArray(ub) {
		return convSameElem(ua, ub, r)
	}
	if convIsSlice(ua) && ub.Kind == KindPointer {
		if ue, err := r.ElemOf(ub); err == nil && ue != nil && convIsArray(r.Underlying(ue)) {
			return convSameElem(ua, r.Underlying(ue), r)
		}
	}
	return false
}

// convBasicName is the canonical basic-type name a peeled typedef
// denotes — "" for composite or unresolvable shapes.
func convBasicName(td *TypeDef) string {
	if td == nil {
		return ""
	}
	if td.Name != "" {
		return CanonicalBasicName(td.Name)
	}
	if id, ok := td.Anon.(*ast.Ident); ok {
		return CanonicalBasicName(id.Name)
	}
	return ""
}

// convIsSlice reports whether a peeled typedef is a slice ([]T, not
// [n]T — both share KindSlice; the AST's Len distinguishes).
func convIsSlice(td *TypeDef) bool {
	if td == nil || td.Kind != KindSlice {
		return false
	}
	if at, ok := td.TypeExpr().(*ast.ArrayType); ok {
		return at.Len == nil
	}
	return true
}

// convIsArray reports whether a peeled typedef is a fixed-length array.
func convIsArray(td *TypeDef) bool {
	if td == nil {
		return false
	}
	at, ok := td.TypeExpr().(*ast.ArrayType)
	return ok && at.Len != nil
}

// convArrayLen spells a fixed-length array's length expr recursively —
// a literal `2`, a named const `N`, or a constant expression `1+1` all
// keep their own spelling (TypLenName's %T fallback would equate
// `1+1` and `2+1`, both *ast.BinaryExpr).
func convArrayLen(td *TypeDef) string {
	at, ok := td.TypeExpr().(*ast.ArrayType)
	if !ok {
		return ""
	}
	return convLenSpelling(at.Len)
}

func convLenSpelling(e ast.Expr) string {
	switch l := e.(type) {
	case nil:
		return ""
	case *ast.BasicLit:
		return l.Value
	case *ast.Ident:
		return l.Name
	case *ast.Ellipsis:
		return "..."
	case *ast.ParenExpr:
		return "(" + convLenSpelling(l.X) + ")"
	case *ast.UnaryExpr:
		return l.Op.String() + convLenSpelling(l.X)
	case *ast.BinaryExpr:
		return convLenSpelling(l.X) + l.Op.String() + convLenSpelling(l.Y)
	case *ast.SelectorExpr:
		return convLenSpelling(l.X) + "." + l.Sel.Name
	}
	return fmt.Sprintf("%T", e)
}

// runeSliceTd reports whether a peeled typedef is []byte or []rune —
// the shapes string converts to and from. A named element counts by
// its underlying name (`type B byte` still converts).
func runeSliceTd(td *TypeDef, r TypeResolver) bool {
	if !convIsSlice(td) {
		return false
	}
	et := convElemOf(td, r)
	if et == nil {
		return false
	}
	switch convBasicName(r.Underlying(et)) {
	case "uint8", "int32":
		return true
	}
	return false
}

// convElemOf resolves a container typedef's element — the resolver's
// ElemOf first, then the typedef's own Elem evidence, then the element
// expr its AST carries (resolveTypeRef-built typedefs keep the
// ArrayType without a materialized Elem).
func convElemOf(td *TypeDef, r TypeResolver) *TypeDef {
	if td == nil {
		return nil
	}
	if et, err := r.ElemOf(td); err == nil && et != nil {
		return et
	}
	if td.Elem != nil {
		return td.Elem
	}
	at, ok := td.TypeExpr().(*ast.ArrayType)
	if !ok || at.Elt == nil {
		return nil
	}
	return resolveElemExpr(td, at.Elt, r)
}

// resolveElemExpr resolves a composite's element expr in the parent's
// context. ResolveType peels *T to T — right for method sets, wrong
// here (`[]*Item`'s element is *Item) — so a pointer expr becomes an
// anonymous pointer typedef carrying the context.
func resolveElemExpr(from *TypeDef, x ast.Expr, r TypeResolver) *TypeDef {
	if px, ok := x.(*ast.ParenExpr); ok {
		return resolveElemExpr(from, px.X, r)
	}
	if _, ok := x.(*ast.StarExpr); ok {
		return &TypeDef{Kind: KindPointer, Anon: x, Pkg: from.Pkg, File: from.File, Binds: from.Binds}
	}
	et, err := r.ResolveType(from, x)
	if err != nil {
		return nil
	}
	return et
}

// convSameElem checks element-typedef identity for the slice->array
// conversion rules.
func convSameElem(a, b *TypeDef, r TypeResolver) bool {
	ea, eb := convElemOf(a, r), convElemOf(b, r)
	return ea != nil && eb != nil && (ea == eb || TypIdentical(ea, eb))
}

// convTagBlind is the recursive comparison behind tag-blind
// conversion: identical types with struct tags erased. Identity here
// is [SigTypEq] — alias-transparent and order-free for interface
// member sets; defined types still differ from the shape they spell
// (`struct{Y S1}` and `struct{Y S2}` do NOT convert even when S1 and
// S2 share a shape). Only anonymous shells recurse, so a tag nested
// inside an anonymous struct still converts. Func signatures keep
// the strict compare — tags inside them are not modeled.
func convTagBlind(a, b *TypeDef, r TypeResolver) bool {
	if a == nil || b == nil {
		return false
	}
	if a == b || SigTypEq(a, b, r) {
		return true
	}
	pa, pb := r.PeelAlias(a), r.PeelAlias(b)
	switch {
	case pa.Kind == KindStruct && pb.Kind == KindStruct &&
		anonShapedTd(pa) && anonShapedTd(pb):
		return convStructTagBlind(pa, pb, r)
	case pa.Kind == KindSlice && pb.Kind == KindSlice &&
		convIsSlice(pa) == convIsSlice(pb) &&
		anonShapedTd(pa) && anonShapedTd(pb):
		// arrays share KindSlice with slices: []T only pairs with []U,
		// [n]T with [n]U (the length compares below).
		ea, eb := convElemOf(pa, r), convElemOf(pb, r)
		if ea == nil || eb == nil {
			return false
		}
		if !convTagBlind(ea, eb, r) {
			return false
		}
		if convIsArray(pa) {
			la, lb := convArrayLen(pa), convArrayLen(pb)
			return la != "" && la == lb
		}
		return true
	case pa.Kind == KindPointer && pb.Kind == KindPointer &&
		anonShapedTd(pa) && anonShapedTd(pb):
		ea, eb := convElemOf(pa, r), convElemOf(pb, r)
		if ea == nil || eb == nil {
			return false
		}
		return convTagBlind(ea, eb, r)
	case pa.Kind == KindMap && pb.Kind == KindMap &&
		anonShapedTd(pa) && anonShapedTd(pb):
		ka, kb := convElemOfKey(pa, r), convElemOfKey(pb, r)
		ea, eb := convElemOf(pa, r), convElemOf(pb, r)
		if ka == nil || kb == nil || ea == nil || eb == nil {
			return false
		}
		return convTagBlind(ka, kb, r) && convTagBlind(ea, eb, r)
	case pa.Kind == KindChan && pb.Kind == KindChan &&
		anonShapedTd(pa) && anonShapedTd(pb):
		da, db := convChanDir(pa), convChanDir(pb)
		if da == 0 || da != db {
			return false
		}
		ea, eb := convElemOf(pa, r), convElemOf(pb, r)
		if ea == nil || eb == nil {
			return false
		}
		return convTagBlind(ea, eb, r)
	}
	return false
}

// convChanDir reads a chan typedef's direction from its AST; 0 means
// unknown (no AST carried) and compares unequal — direction is part of
// identity.
func convChanDir(td *TypeDef) ast.ChanDir {
	if ct, ok := td.TypeExpr().(*ast.ChanType); ok {
		return ct.Dir
	}
	return 0
}

// anonShapedTd reports whether a typedef spells its shape anonymously
// — identity-ignoring-tags recurses only through unnamed shells;
// defined types keep their declared identity.
func anonShapedTd(td *TypeDef) bool {
	return td.Spec == nil && td.Name == ""
}

// anonShell re-wraps a peeled underlying typedef without its declared
// name/spec, so the top level of convTagBlind compares the shape the
// declaration spells. The minted typedef is only compared — never
// interned.
func anonShell(td *TypeDef) *TypeDef {
	if td == nil || anonShapedTd(td) {
		return td
	}
	return &TypeDef{Kind: td.Kind, Anon: td.TypeExpr(), Pkg: td.Pkg, File: td.File, Binds: td.Binds,
		LocalTypes: td.LocalTypes, Fields: td.Fields, FTags: td.FTags, EmbedIdx: td.EmbedIdx}
}

// convStructTagBlind compares two struct shapes field by field: names
// must match exactly while each field's type compares tag-blind. The
// field type exprs come from the struct's own AST; a typedef without
// one keeps the names-only approximation.
func convStructTagBlind(ua, ub *TypeDef, r TypeResolver) bool {
	if !StructFieldsEq(ua, ub) {
		return false
	}
	sa, sok := ua.TypeExpr().(*ast.StructType)
	sb, sok2 := ub.TypeExpr().(*ast.StructType)
	if !sok || !sok2 {
		return true // no field AST to compare — names-only approximation
	}
	ta := syntax.FieldTypes(sa.Fields)
	tb := syntax.FieldTypes(sb.Fields)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		ea, eb := resolveElemExpr(ua, ta[i], r), resolveElemExpr(ub, tb[i], r)
		if ea == nil || eb == nil {
			return false
		}
		if !convTagBlind(ea, eb, r) {
			return false
		}
	}
	return true
}

// convElemOfKey resolves a map typedef's key typedef the same way
// convElemOf resolves its element.
func convElemOfKey(td *TypeDef, r TypeResolver) *TypeDef {
	mt, ok := td.TypeExpr().(*ast.MapType)
	if !ok || mt.Key == nil {
		return nil
	}
	return resolveElemExpr(td, mt.Key, r)
}
