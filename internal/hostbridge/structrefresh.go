package hostbridge

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

// refreshableMethod is a pure method whose result cannot drag another
// struct projection — and that projection's eager methods — into the
// refresh. Composite results are left at their first projection.
func refreshableMethod(m *runtime.Function) bool {
	if !pureMethod(m) || m.Decl.Type == nil || m.Decl.Type.Results == nil {
		return false
	}
	results := m.Decl.Type.Results.List
	if len(results) != 1 && len(results) != 2 {
		return false
	}
	for _, r := range results {
		if len(r.Names) > 1 {
			return false
		}
	}
	id, ok := results[0].Type.(*ast.Ident)
	if !ok {
		return false
	}
	switch id.Name {
	case "bool", "string",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128":
	default:
		return false
	}
	if len(results) == 2 {
		id, ok := results[1].Type.(*ast.Ident)
		return ok && id.Name == "error"
	}
	return true
}

// noteImpureMethodCallback marks the mutation barrier for a method call
// that may write. Expression-only methods cannot mutate state, so their
// refresh does not make the projection stale again.
func (m *Marshal) noteImpureMethodCallback(f *runtime.Function) {
	if !pureMethod(f) {
		m.noteScriptCallback()
	}
}
