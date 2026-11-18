package minigo

import (
	"context"
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// EvalExpr evaluates expr in pkg's scope, resolving names as a function
// body in file would: file's imports, package globals and builtins.
func (e *Engine) EvalExpr(ctx context.Context, pkg *runtime.Package, file *syntax.File, expr ast.Expr) (runtime.Value, error) {
	// same dev-time guard as Call (M3-1): initializing a foreign session's
	// package on this engine's VM would alias its globals across sessions.
	if pkg.Owner != nil && pkg.Owner != e {
		return nil, fmt.Errorf("minigo: package %q belongs to a different session", pkg.Path)
	}
	// The fragment travels as AST inside a chunk (OP_EVAL_AST) and compiles
	// via compile.Expr on first execution.
	vmm := e.newVM()
	vmm.EnsureProc()
	defer vmm.ReleaseProc()
	if err := pkg.EnsureReadyRun(func(fn *runtime.Function) error {
		_, err := vmm.Call(fn, nil)
		return err
	}); err != nil {
		return nil, err
	}
	ch := &bytecode.Chunk{
		Name:   "<eval>",
		Consts: []any{&bytecode.ASTFragment{Expr: expr, File: file}},
		Code: []bytecode.Instruction{
			{Op: bytecode.OpEvalAST, A: 0, C: -1},
			{Op: bytecode.OpReturn, A: 1, C: -1},
		},
	}
	return vmm.Call(&runtime.Function{Pkg: pkg, File: file, Name: "<eval>", Chunk: ch}, nil)
}
