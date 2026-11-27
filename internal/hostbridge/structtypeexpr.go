package hostbridge

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

func typeSpecOf(m *runtime.Function, name string) *ast.TypeSpec {
	if m == nil || m.Pkg == nil || m.Pkg.Index == nil {
		return nil
	}
	return m.Pkg.Index.TypeSpec(name)
}

func scalarTypeName(name string) bool {
	switch name {
	case "bool", "string", "byte", "rune",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128":
		return true
	}
	return false
}

// typeExprMatches follows named types until match; unknown shapes are false.
func typeExprMatches(m *runtime.Function, x ast.Expr, seen map[string]bool, match func(ast.Expr) bool) bool {
	if match(x) {
		return true
	}
	switch x := x.(type) {
	case *ast.ParenExpr:
		return typeExprMatches(m, x.X, seen, match)
	case *ast.Ident:
		if seen[x.Name] {
			return false
		}
		ts := typeSpecOf(m, x.Name)
		if ts == nil {
			return false
		}
		seen[x.Name] = true
		return typeExprMatches(m, ts.Type, seen, match)
	}
	return false
}

// scalarTypeExpr reports whether x is a predeclared scalar or a package
// type whose underlying type resolves to one.
func scalarTypeExpr(m *runtime.Function, x ast.Expr, seen map[string]bool) bool {
	return typeExprMatches(m, x, seen, func(x ast.Expr) bool {
		id, ok := x.(*ast.Ident)
		return ok && scalarTypeName(id.Name)
	})
}
