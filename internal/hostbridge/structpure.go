package hostbridge

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

// pureMethod reports whether a script method is a simple receiver-value
// computation. Unknown effects stay impure; type conversions are pure.
func pureMethod(m *runtime.Function) bool {
	if m == nil || m.Decl == nil || m.Decl.Body == nil {
		return false
	}
	pure := true
	ast.Inspect(m.Decl.Body, func(n ast.Node) bool {
		if !pure {
			return false
		}
		if effectfulNode(m, n) {
			pure = false
			return false
		}
		return true
	})
	return pure
}
