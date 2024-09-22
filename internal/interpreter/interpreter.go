package interpreter

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"strings"
)

func New(fset *token.FileSet, entryPoint string, stdout, stderr io.Writer) *Interpreter {
	return &Interpreter{
		fset:       fset,
		entryPoint: entryPoint,
		evaluator:  &evaluator{stdout: stdout, stderr: stderr, scope: &scope{frames: []map[string]string{{}}}},
	}
}

type Interpreter struct {
	fset       *token.FileSet
	entryPoint string
	evaluator  *evaluator
}

func (app *Interpreter) RunFile(ctx context.Context, file *ast.File) error {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Name.Name == app.entryPoint {
				return app.runFunc(ctx, fn)
			}
		}
	}
	return fmt.Errorf("entrypoint func %s() is not found", app.entryPoint)
}

func (app *Interpreter) runFunc(ctx context.Context, fn *ast.FuncDecl) error {
	// TODO: handling arguments
	for lines := fn.Body.List; len(lines) > 0; lines = lines[1:] {
		if err := app.evaluator.EvalStmt(ctx, lines[0]); err != nil {
			return fmt.Errorf("line:%d failed to eval stmt: %w", app.fset.Position(lines[0].Pos()).Line, err)
		}
	}
	return nil
}

type scope struct {
	frames []map[string]string
}

func (s *scope) Push() {
	s.frames = append(s.frames, map[string]string{})
}
func (s *scope) Pop() {
	s.frames = s.frames[:len(s.frames)-1]
}

func (s *scope) Set(name string, value string) {
	s.frames[len(s.frames)-1][name] = value
}

func (s *scope) Get(name string) (string, bool) {
	for n := len(s.frames) - 1; n >= 0; n-- {
		if val, ok := s.frames[n][name]; ok {
			return val, true
		}
	}
	return "", false
}

type evaluator struct {
	stdout io.Writer
	stderr io.Writer
	scope  *scope
}

func (e *evaluator) EvalStmt(ctx context.Context, stmt ast.Stmt) error {
	switch stmt := stmt.(type) {
	case *ast.ExprStmt:
		if _, err := e.EvalExpr(ctx, stmt.X); err != nil {
			return fmt.Errorf("failed to eval expr: %w", err)
		}
		return nil
	case *ast.BlockStmt:
		e.scope.Push()
		defer e.scope.Pop()
		for i, sub := range stmt.List {
			if err := e.EvalStmt(ctx, sub); err != nil {
				return fmt.Errorf("in block %d: %w", i, err)
			}
		}
		return nil
	case *ast.AssignStmt:
		// only support <lhs> := <rhs>
		if len(stmt.Lhs) > 1 {
			return fmt.Errorf("unsupported assign lhs %s", strings.Repeat("<var> ", len(stmt.Lhs)))
		}
		if len(stmt.Rhs) > 1 {
			return fmt.Errorf("unsupported assign rhs %s", strings.Repeat("<var> ", len(stmt.Rhs)))
		}

		lhs := stmt.Lhs[0]
		rhs := stmt.Rhs[0]
		switch lhs := lhs.(type) {
		case *ast.Ident:
			val, err := e.EvalExpr(ctx, rhs)
			if err != nil {
				return fmt.Errorf("failed to eval assign: %w", err)
			}
			e.scope.Set(lhs.Name, val)
			return nil
		default:
			return fmt.Errorf("unsupported assign lhs type: %T", lhs)
		}
	default:
		return fmt.Errorf("unsupported stmt type: %T", stmt)
	}
}

func (e *evaluator) EvalExpr(ctx context.Context, expr ast.Expr) (string, error) {
	switch expr := expr.(type) {
	case *ast.BasicLit:
		// only support string literal
		return expr.Value[1 : len(expr.Value)-1], nil // TODO: fix
	case *ast.Ident:
		val, ok := e.scope.Get(expr.Name)
		if !ok {
			return "", fmt.Errorf("undefined variable: %s", expr.Name)
		}
		return val, nil
	case *ast.BinaryExpr:
		return e.evalBinaryExpr(ctx, expr)
	case *ast.CallExpr:
		return e.evalCallExpr(ctx, expr)
	default:
		return "", fmt.Errorf("unsupported expr type: %T", expr)
	}
}

func (e *evaluator) evalCallExpr(ctx context.Context, expr *ast.CallExpr) (string, error) {
	args := make([]any, 0, len(expr.Args))
	for i, arg := range expr.Args {
		val, err := e.EvalExpr(ctx, arg)
		if err != nil {
			return "", fmt.Errorf("failed to eval argument[%d]: %w", i, err)
		} else {
			args = append(args, val)
		}
	}

	// TODO: fix (currently, only support println() and fmt.Println())
	switch fun := expr.Fun.(type) {
	case *ast.Ident:
		ident := fun
		// only support println()
		if ident.Name == "println" {
			fmt.Fprintln(e.stdout, args...)
			return "", nil
		} else {
			return "", fmt.Errorf("unsupported function: %s", ident.Name)
		}
	case *ast.SelectorExpr:
		sel := fun
		// only support fmt.Println() and strings.ToUpper()
		if ident, ok := sel.X.(*ast.Ident); ok {
			if ident.Name == "fmt" && sel.Sel.Name == "Println" {
				fmt.Fprintln(e.stdout, args...)
				return "", nil
			} else if ident.Name == "strings" && sel.Sel.Name == "ToUpper" {
				return strings.ToUpper(args[0].(string)), nil
			} else {
				return "", fmt.Errorf("unsupported function: %s.%s", sel.X, sel.Sel.Name)
			}
		} else {
			return "", fmt.Errorf("unsupported function:: %s.%s", sel.X, sel.Sel.Name)
		}
	default:
		return "", fmt.Errorf("unsupported function: %T", expr.Fun)
	}
}

func (e *evaluator) evalBinaryExpr(ctx context.Context, expr *ast.BinaryExpr) (string, error) {
	x, err := e.EvalExpr(ctx, expr.X)
	if err != nil {
		return "", fmt.Errorf("failed to eval binary expr lhs: %w", err)
	}
	y, err := e.EvalExpr(ctx, expr.Y)
	if err != nil {
		return "", fmt.Errorf("failed to eval binary expr rhs: %w", err)
	}

	// only support ADD
	switch expr.Op {
	case token.ADD:
		return x + y, nil
	default:
		return "", fmt.Errorf("unsupported operator: %v", expr.Op)
	}
}
