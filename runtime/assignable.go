package runtime

import (
	"go/ast"
	"strings"
)

// ShapeEq is the assignability comparator behind the VM's shape checks:
// whether two typedefs have the same underlying shape (conversion and
// typed-nil retagging), and whether two anonymous pointer typedefs point
// at the same type once aliases peel. The engine lookups it needs —
// alias targets, declared-chain underlying, element resolution — come
// through the injected [TypeResolver]; resolvers without those
// capabilities answer by spelling alone.
type ShapeEq struct {
	res TypeResolver
}

// NewShapeEq builds the comparator over a resolver (nil allowed —
// identity falls back to spellings, which can only report fewer types
// as equal).
func NewShapeEq(res TypeResolver) ShapeEq {
	return ShapeEq{res: orResolver(res)}
}

// Same reports whether two typedefs have the same underlying shape —
// for typed-nil retagging (`var s S = ([]int)(nil)` needs S ~ []int).
// Shape spelling normalizes byte/rune and resolves bound type params
// (Conv), so []byte and []uint8 count as one shape.
func (c ShapeEq) Same(a, b *TypeDef) bool {
	pa, pb := c.res.Underlying(a), c.res.Underlying(b)
	if pa == pb {
		return true
	}
	if pa == nil || pb == nil || pa.Kind != pb.Kind {
		return false
	}
	if TypIdentical(pa, pb) {
		return true
	}
	if c.Conv(pa, pb) {
		return true
	}
	// the shape spelling comes from the anonymous type AST, or the
	// declaring spec's for a named type — `map[string]int` spelled
	// anonymously and as `type M map[string]int` is one shape.
	src := func(t *TypeDef) ast.Expr {
		return t.TypeExpr()
	}
	sa, sb := src(pa), src(pb)
	if sa != nil && sb != nil {
		// not implied by Conv: the spelling resolves type
		// params through each side's binds, so `[]E` with E bound to
		// KeyValue matches `type SortedMap []KeyValue` (a generic
		// `S ~[]E` argument passed on as a `[]E` parameter).
		return TypSpelling(sa, pa) == TypSpelling(sb, pb)
	}
	return sa == nil && sb == nil
}

// Conv reports whether two typedefs have identical underlying
// shape for a conversion: both peel through declared chains to their
// structural spelling — `type C B` gives B's shape, `Wrap[int]` gives
// []int — while element positions compare by written identity, so []MyInt
// does not spell []int.
func (c ShapeEq) Conv(a, b *TypeDef) bool {
	sa, sb := c.UnderlyingShape(a), c.UnderlyingShape(b)
	return sa != "" && sa == sb
}

// UnderlyingShape spells a typedef's underlying shape. Declared chain
// heads (type C B, aliases) peel away; composite shapes spell their type
// expression with generic binds substituted.
func (c ShapeEq) UnderlyingShape(td *TypeDef) string {
	u := c.res.Underlying(td)
	if u == nil {
		return ""
	}
	if s := TypUnderlyingSpelling(u); s != "" {
		return s
	}
	// a typedef synthesized at a boundary (`&x` yields
	// {Kind: KindPointer, Elem: pointee}) carries its shape as Kind+Elem
	// with no Anon/Spec/Name to spell — rebuild it from those.
	return c.synthShape(u)
}

// PointeeAlias reports whether two anonymous pointer typedefs point
// at the same type once aliases peel — a nil *inner.Tree binds *tree for
// `type tree = inner.Tree` (kin-openapi's originTree).
func (c ShapeEq) PointeeAlias(a, b *TypeDef) bool {
	if a == nil || b == nil || a.Kind != KindPointer || b.Kind != KindPointer ||
		a.Spec != nil || b.Spec != nil {
		return false
	}
	ea, err := c.res.ElemOf(a)
	if err != nil || ea == nil {
		return false
	}
	eb, err := c.res.ElemOf(b)
	if err != nil || eb == nil {
		return false
	}
	return TypIdentical(c.res.PeelAlias(ea), c.res.PeelAlias(eb))
}

// synthShape spells a typedef that has no AST or name: only Kind and the
// resolved Elem chain. Element positions keep the declared identity, the
// same way TypSpelling renders a bound type argument (`*N`, not `*int`).
func (c ShapeEq) synthShape(td *TypeDef) string {
	if td == nil {
		return ""
	}
	if td.Name != "" {
		if td.Pkg != nil {
			if strings.HasPrefix(td.Name, td.Pkg.Path+".") {
				return td.Name
			}
			return td.Pkg.Path + "." + td.Name
		}
		return CanonicalBasicName(td.Name)
	}
	// an anonymous element still spells its own AST — *struct{a int}
	// and *struct{b string} must not collapse to the same "*".
	src := td.TypeExpr()
	if src != nil {
		return TypSpelling(src, td)
	}
	var elem string
	if td.Elem != nil {
		elem = c.synthShape(td.Elem)
	}
	switch td.Kind {
	case KindPointer:
		return "*" + elem
	case KindSlice:
		return "[]" + elem
	case KindChan:
		return "chan " + elem
	}
	return ""
}

// StructFieldsEq compares two struct typedefs by field name lists —
// the approximate "identical underlying" for an unnamed struct literal
// binding a named type (Go also requires matching tags and element
// types; neither is modeled here).
func StructFieldsEq(a, b *TypeDef) bool {
	if len(a.Fields) != len(b.Fields) {
		return false
	}
	for i := range a.Fields {
		if a.Fields[i] != b.Fields[i] {
			return false
		}
	}
	return true
}

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
	// tag-blind struct conversion (Go 1.8+): identical underlying struct
	// shapes convert even when field tags differ — unlike assignment.
	if ua.Kind == KindStruct && ub.Kind == KindStruct && StructFieldsEq(ua, ub) {
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
	if ca == 's' && runeSliceTd(ub) {
		return true // string -> []byte / []rune
	}
	if cb == 's' && runeSliceTd(ua) {
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

// runeSliceTd reports whether a peeled typedef is []byte or []rune —
// the shapes string converts to and from.
func runeSliceTd(td *TypeDef) bool {
	if !convIsSlice(td) {
		return false
	}
	et := td.Elem
	if et == nil {
		return false
	}
	switch convBasicName(et) {
	case "uint8", "int32":
		return true
	}
	return false
}

// convSameElem checks element-typedef identity for the slice->array
// conversion rules.
func convSameElem(a, b *TypeDef, r TypeResolver) bool {
	ea, err := r.ElemOf(a)
	if err != nil || ea == nil {
		ea = a.Elem
	}
	eb, err := r.ElemOf(b)
	if err != nil || eb == nil {
		eb = b.Elem
	}
	return ea != nil && eb != nil && (ea == eb || TypIdentical(ea, eb))
}
