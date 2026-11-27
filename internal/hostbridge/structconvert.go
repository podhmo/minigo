package hostbridge

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

func predeclaredTypeName(name string) bool {
	return scalarTypeName(name) || name == "any" || name == "error"
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
