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

// scalarTypeExpr reports whether x is a predeclared scalar or a package
// type whose underlying type resolves to one. Composite and unknown types
// are false so refresh stays conservative.
func scalarTypeExpr(m *runtime.Function, x ast.Expr, seen map[string]bool) bool {
	switch x := x.(type) {
	case *ast.ParenExpr:
		return scalarTypeExpr(m, x.X, seen)
	case *ast.Ident:
		switch x.Name {
		case "bool", "string", "byte", "rune",
			"int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
			"float32", "float64", "complex64", "complex128":
			return true
		}
		if seen[x.Name] {
			return false
		}
		ts := typeSpecOf(m, x.Name)
		if ts == nil {
			return false
		}
		seen[x.Name] = true
		return scalarTypeExpr(m, ts.Type, seen)
	}
	return false
}

func predeclaredTypeName(name string) bool {
	switch name {
	case "bool", "string", "byte", "rune", "any", "error",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128":
		return true
	}
	return false
}

// typeConversionCall reports whether call is a conversion rather than a
// function invocation. Conversions can allocate, but they do not mutate
// script state, so they do not make a getter impure.
func typeConversionCall(m *runtime.Function, call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	if predeclaredTypeName(id.Name) || typeSpecOf(m, id.Name) != nil {
		return true
	}
	for _, name := range m.TParams {
		if id.Name == name {
			return true
		}
	}
	return false
}
