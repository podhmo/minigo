package syntax

import (
	"go/ast"
	"go/token"
)

// LocalScopes records where each name bound below package scope is in
// scope in a file, following the spec's scoping rules:
//
//   - type parameters, the receiver's included, over their whole func or
//     type decl;
//   - params, results and receivers over the func body only, so a
//     signature's types still name package-level or imported entities;
//   - := and local var/const names from the end of their statement, so
//     `min := min(1, 2)` calls the builtin on the right-hand side;
//   - local types from their name, so they can be recursive;
//   - range bindings over the loop body;
//
// each up to the end of the innermost enclosing block, if/for/switch,
// select or case clause.
type LocalScopes struct {
	spans map[string][]span
}

type span struct{ from, to token.Pos }

// NewLocalScopes collects the local bindings of f.
func NewLocalScopes(f *ast.File) *LocalScopes {
	s := &LocalScopes{spans: map[string][]span{}}
	bind := func(name string, from, to token.Pos) {
		if name != "_" && to != token.NoPos {
			s.spans[name] = append(s.spans[name], span{from, to})
		}
	}
	bindFields := func(fl *ast.FieldList, body *ast.BlockStmt) {
		if fl == nil || body == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				bind(n.Name, body.Pos(), body.End())
			}
		}
	}
	var stack []ast.Node
	// scopeEnd is the end of the innermost node on the stack that opens
	// a scope.
	scopeEnd := func() token.Pos {
		for i := len(stack) - 1; i >= 0; i-- {
			switch n := stack[i].(type) {
			case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
				*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt,
				*ast.CaseClause, *ast.CommClause:
				return n.End()
			}
		}
		return token.NoPos
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		switch x := n.(type) {
		case *ast.FuncDecl:
			for _, name := range RecvTypeParamNames(x.Recv) {
				bind(name, x.Pos(), x.End())
			}
			for _, name := range TypeParamNames(x.Type.TypeParams) {
				bind(name, x.Pos(), x.End())
			}
			bindFields(x.Recv, x.Body)
			bindFields(x.Type.Params, x.Body)
			bindFields(x.Type.Results, x.Body)
		case *ast.FuncLit:
			bindFields(x.Type.Params, x.Body)
			bindFields(x.Type.Results, x.Body)
		case *ast.TypeSpec:
			for _, name := range TypeParamNames(x.TypeParams) {
				bind(name, x.Pos(), x.End())
			}
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						bind(id.Name, x.End(), scopeEnd())
					}
				}
			}
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE {
				for _, e := range []ast.Expr{x.Key, x.Value} {
					if id, ok := e.(*ast.Ident); ok {
						bind(id.Name, x.Body.Pos(), x.Body.End())
					}
				}
			}
		case *ast.DeclStmt:
			if gd, ok := x.Decl.(*ast.GenDecl); ok {
				end := scopeEnd()
				for _, spec := range gd.Specs {
					switch sp := spec.(type) {
					case *ast.ValueSpec:
						for _, id := range sp.Names {
							bind(id.Name, sp.End(), end)
						}
					case *ast.TypeSpec:
						bind(sp.Name.Name, sp.Name.Pos(), end)
					}
				}
			}
		}
		stack = append(stack, n)
		return true
	})
	return s
}

// At reports whether a local binding of name is in scope at pos.
func (s *LocalScopes) At(name string, pos token.Pos) bool {
	for _, sp := range s.spans[name] {
		if sp.from <= pos && pos < sp.to {
			return true
		}
	}
	return false
}
