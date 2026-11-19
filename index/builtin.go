package index

import "go/ast"

// BuiltinMakeType returns the result type spelled by a call to the
// predeclared make in a package-level initializer. Its size arguments
// do not contribute to the type.
func (ix *Index) BuiltinMakeType(e ast.Expr) ast.Expr {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil
	}
	fun, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || fun.Name != "make" {
		return nil
	}
	if _, shadowed := ix.Lookup(fun.Name); shadowed {
		return nil
	}
	return call.Args[0]
}
