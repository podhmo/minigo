package index

import (
	"go/ast"
	"go/token"
	"iter"
)

// EffectiveValueSpec pairs the original spec with its effective type and values.
// For an empty const spec, Type and Values come from the preceding non-empty
// spec. The AST nodes are shared with the original declaration, not copied.
type EffectiveValueSpec struct {
	Spec   *ast.ValueSpec
	Type   ast.Expr
	Values []ast.Expr
}

// ValueSpecs yields var or const specs with their original indices (for iota).
// Within each const declaration, an omitted expression list repeats the first
// preceding non-empty list and its type, if any. Var specs never inherit.
// Other kinds of declarations yield nothing. The original AST is not modified.
func ValueSpecs(d *ast.GenDecl) iter.Seq2[int, EffectiveValueSpec] {
	return func(yield func(int, EffectiveValueSpec) bool) {
		if d.Tok != token.VAR && d.Tok != token.CONST {
			return
		}
		var prevValues []ast.Expr
		var prevType ast.Expr
		for i, spec := range d.Specs {
			vs := spec.(*ast.ValueSpec)
			values, typ := vs.Values, vs.Type
			if d.Tok == token.CONST {
				if len(values) == 0 {
					values, typ = prevValues, prevType
				} else {
					prevValues, prevType = values, typ
				}
			}
			if !yield(i, EffectiveValueSpec{Spec: vs, Type: typ, Values: values}) {
				return
			}
		}
	}
}
