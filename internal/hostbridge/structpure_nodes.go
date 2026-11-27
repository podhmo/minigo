package hostbridge

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/runtime"
)

// effectfulNode reports one AST node's direct state effect. Inspect still
// visits the node's children after a false result.
func effectfulNode(m *runtime.Function, n ast.Node) bool {
	switch x := n.(type) {
	case *ast.AssignStmt, *ast.IncDecStmt, *ast.SendStmt, *ast.GoStmt, *ast.DeferStmt, *ast.SelectStmt:
		return true
	case *ast.CallExpr:
		return !typeConversionCall(m, x)
	case *ast.RangeStmt:
		return x.Tok == token.ASSIGN || !rangeKnownNonChannel(m, x.X)
	case *ast.UnaryExpr:
		return x.Op == token.ARROW
	}
	return false
}
