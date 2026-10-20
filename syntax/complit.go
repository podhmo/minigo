package syntax

import "go/ast"

// PeelLitType peels a composite literal's declared type depth levels
// (array elt / map value / pointer / ellipsis) — the element type a
// nested literal with an elided type (`{...}` inside `[][]int{...}`)
// takes its shape from. Parens are transparent; any other node, a named
// type included, peels to nil: resolving names is the caller's job.
func PeelLitType(t ast.Expr, depth int) ast.Expr {
	for i := 0; t != nil && i < depth; i++ {
		switch tt := t.(type) {
		case *ast.ArrayType:
			t = tt.Elt
		case *ast.MapType:
			t = tt.Value
		case *ast.StarExpr:
			t = tt.X
		case *ast.Ellipsis:
			t = tt.Elt
		case *ast.ParenExpr:
			t = tt.X
			i-- // parens don't count as a peel level
		default:
			t = nil
		}
	}
	return t
}
