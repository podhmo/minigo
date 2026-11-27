package hostbridge

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/runtime"
)

// receiverShadowed reports whether an inner declaration can make the
// receiver spelling resolve to something else. It is conservative across
// scopes because hostbridge deliberately avoids a type-checking pass.
func receiverShadowed(m *runtime.Function, name string, exclude *ast.RangeStmt) bool {
	if name == "" || m == nil || m.Decl == nil || m.Decl.Body == nil {
		return true
	}
	shadowed := false
	ast.Inspect(m.Decl.Body, func(n ast.Node) bool {
		if shadowed {
			return false
		}
		has := func(names ...string) bool {
			for _, got := range names {
				if got == name {
					return true
				}
			}
			return false
		}
		switch x := n.(type) {
		case *ast.ValueSpec:
			for _, n := range x.Names {
				shadowed = shadowed || has(n.Name)
			}
		case *ast.TypeSpec:
			shadowed = shadowed || has(x.Name.Name)
		case *ast.RangeStmt:
			if x != exclude && x.Tok == token.DEFINE {
				for _, e := range []ast.Expr{x.Key, x.Value} {
					if id, ok := e.(*ast.Ident); ok {
						shadowed = shadowed || has(id.Name)
					}
				}
			}
		case *ast.FuncLit:
			if x.Type.Params != nil {
				for _, f := range x.Type.Params.List {
					for _, n := range f.Names {
						shadowed = shadowed || has(n.Name)
					}
				}
			}
		}
		return !shadowed
	})
	return shadowed
}
