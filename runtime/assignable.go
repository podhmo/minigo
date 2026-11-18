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
