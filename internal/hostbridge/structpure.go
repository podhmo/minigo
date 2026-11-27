package hostbridge

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/runtime"
)

// pureMethod reports whether a script method is a simple receiver-value
// computation. It is deliberately conservative: any call, assignment, or
// control effect can mutate state, so only expression-only methods are
// refreshed eagerly. That refreshes getters such as `return p.N` after a
// callback mutation without running methods with side effects twice.
func pureMethod(m *runtime.Function) bool {
	if m == nil || m.Decl == nil || m.Decl.Body == nil {
		return false
	}
	pure := true
	ast.Inspect(m.Decl.Body, func(n ast.Node) bool {
		if !pure {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr, *ast.AssignStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.GoStmt, *ast.DeferStmt, *ast.RangeStmt, *ast.SelectStmt:
			pure = false
			return false
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				pure = false
				return false
			}
		}
		return true
	})
	return pure
}
