package main

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
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

	app := &app{
		fset:       fset,
		entryPoint: entryPoint,
		evaluator:  &evaluator{},
	}
	return app.runFile(ctx, node)
}

type app struct {
	fset       *token.FileSet
	entryPoint string
	evaluator  *evaluator
}

func (app *app) runFile(ctx context.Context, file *ast.File) error {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Name.Name == app.entryPoint {
				return app.runFunc(ctx, fn)
			}
		}
	}
	return fmt.Errorf("entrypoint func %s() is not found", app.entryPoint)
}

func (app *app) runFunc(ctx context.Context, fn *ast.FuncDecl) error {
	// TODO: handling arguments
	for lines := fn.Body.List; len(lines) > 0; lines = lines[1:] {
		if stmt, ok := lines[0].(*ast.ExprStmt); ok {
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if _, err := app.evaluator.evalCallExpr(ctx, call); err != nil {
					return err // TODO: line number
				}
			} else {
				return fmt.Errorf("unsupported expr: %T", stmt.X) // TODO: line number
			}
		} else {
			return fmt.Errorf("unsupported statement: %T", lines[0]) // TODO
		}
	}
	return nil
}

type evaluator struct {
}

func (e *evaluator) evalCallExpr(ctx context.Context, expr *ast.CallExpr) (string, error) {
	args := make([]any, 0, len(expr.Args))
	for i, arg := range expr.Args {
		switch arg := arg.(type) {
		case *ast.BasicLit:
			val, err := e.evalValue(ctx, arg)
			if err != nil {
				return "", fmt.Errorf("failed to eval argument[%d]: %w", i, err) // TODO: line number
			}
			args = append(args, val)
		case *ast.BinaryExpr:
			val, err := e.evalBinaryExpr(ctx, arg)
			if err != nil {
				return "", err
			}
			args = append(args, val)
		case *ast.CallExpr:
			val, err := e.evalCallExpr(ctx, arg)
			if err != nil {
				return "", err
			}
			args = append(args, val)
		default:
			return "", fmt.Errorf("unsupported argument[%d] type: %T", i, arg) // TODO: line number
		}
	}

	// TODO: fix (currently, only support println() and fmt.Println())
	if ident, ok := expr.Fun.(*ast.Ident); ok { // println()
		if ident.Name == "println" {
			fmt.Println(args...)
		} else {
			return "", fmt.Errorf("unsupported function: %s", ident.Name)
		}
	} else if sel, ok := expr.Fun.(*ast.SelectorExpr); ok { // fmt.Println()
		if ident, ok := sel.X.(*ast.Ident); ok {
			if ident.Name == "fmt" && sel.Sel.Name == "Println" {
				fmt.Println(args...)
			}
		} else {
			return "", fmt.Errorf("unsupported function: %s.%s", sel.Sel.Name, sel.X)
		}
	}

	return "", nil
}

func (e *evaluator) evalBinaryExpr(ctx context.Context, expr *ast.BinaryExpr) (string, error) {
	x, err := e.evalValue(ctx, expr.X)
	if err != nil {
		return "", err
	}
	y, err := e.evalValue(ctx, expr.Y)
	if err != nil {
		return "", err
	}

	switch expr.Op {
	case token.ADD:
		return x + y, nil
	default:
		return "", fmt.Errorf("unsupported operator: %v", expr.Op)
	}
}

func (e *evaluator) evalValue(ctx context.Context, expr ast.Expr) (string, error) { // TODO: return any
	switch expr := expr.(type) {
	case *ast.BasicLit:
		return expr.Value[1 : len(expr.Value)-1], nil // TODO: fix
	case *ast.BinaryExpr:
		val, err := e.evalBinaryExpr(ctx, expr)
		if err != nil {
			return "", err
		}
		return val, nil
	default:
		return "-", fmt.Errorf("unsupported value type: %T", expr)
	}
}
