package main

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"strings"
)

func main() {
	var options struct {
		entrypoint string
	}

	flag.StringVar(&options.entrypoint, "entrypoint", "main", "entrypoint function name")
	flag.Parse()

	if len(flag.Args()) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: minigo <filename>")
		return
	}

	ctx := context.Background()
	// logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	filename := flag.Args()[0]
	if err := run(ctx, filename, options.entrypoint); err != nil {
		logger.ErrorContext(ctx, "failed to run", "error", err)
		return
	}
}

func run(ctx context.Context, filename string, entryPoint string) error {
	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, filename, nil, parser.AllErrors)
	if err != nil {
		return fmt.Errorf("failed to parse file: %w", err)
	}

	app := &App{
		fset:       fset,
		entryPoint: entryPoint,
		evaluator:  &evaluator{stdout: os.Stdout, stderr: os.Stderr},
	}
	return app.runFile(ctx, node)
}

type App struct {
	fset       *token.FileSet
	entryPoint string
	evaluator  *evaluator
}

func (app *App) runFile(ctx context.Context, file *ast.File) error {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Name.Name == app.entryPoint {
				return app.runFunc(ctx, fn)
			}
		}
	}
	return fmt.Errorf("entrypoint func %s() is not found", app.entryPoint)
}

func (app *App) runFunc(ctx context.Context, fn *ast.FuncDecl) error {
	// TODO: handling arguments
	for lines := fn.Body.List; len(lines) > 0; lines = lines[1:] {
		if err := app.evaluator.EvalStmt(ctx, lines[0]); err != nil {
			return fmt.Errorf("line:%d failed to eval stmt: %w", app.fset.Position(lines[0].Pos()).Line, err)
		}
	}
	return nil
}

type evaluator struct {
	stdout io.Writer
	stderr io.Writer
}

func (e *evaluator) EvalStmt(ctx context.Context, stmt ast.Stmt) error {
	switch stmt := stmt.(type) {
	case *ast.ExprStmt:
		if _, err := e.EvalExpr(ctx, stmt.X); err != nil {
			return fmt.Errorf("failed to eval expr: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported stmt type: %T", stmt)
	}
}

func (e *evaluator) EvalExpr(ctx context.Context, expr ast.Expr) (string, error) {
	switch expr := expr.(type) {
	case *ast.BasicLit:
		// only support string literal
		return expr.Value[1 : len(expr.Value)-1], nil // TODO: fix
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
		return "", err
	}
	y, err := e.EvalExpr(ctx, expr.Y)
	if err != nil {
		return "", err
	}

	// only support ADD
	switch expr.Op {
	case token.ADD:
		return x + y, nil
	default:
		return "", fmt.Errorf("unsupported operator: %v", expr.Op)
	}
}
