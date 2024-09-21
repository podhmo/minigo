package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
)

func main() {
	ctx := context.Background()
	entryPoint := "main"

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: minigo <filename>")
		return
	}

	// logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	filename := os.Args[1]
	if err := run(ctx, filename, entryPoint); err != nil {
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
	return nil
}

func (app *app) runFunc(ctx context.Context, fn *ast.FuncDecl) error {
	// TODO: handling arguments
	for line := fn.Body.List; len(line) > 0; line = line[1:] {
		if stmt, ok := line[0].(*ast.ExprStmt); ok {
			if call, ok := stmt.X.(*ast.CallExpr); ok {
				if ident, ok := call.Fun.(*ast.Ident); ok { // println()
					if ident.Name == "println" {
						if err := app.evaluator.evalPrintln(ctx, call); err != nil {
							return err // TODO: line number
						}
					}
				} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok { // fmt.Println()
					if ident, ok := sel.X.(*ast.Ident); ok {
						if ident.Name == "fmt" && sel.Sel.Name == "Println" {
							if err := app.evaluator.evalPrintln(ctx, call); err != nil {
								return err // TODO: line number
							}
						}
					}
				}
			}
		}
	}
	return nil
}

type evaluator struct {
}

func (e *evaluator) evalPrintln(ctx context.Context, call *ast.CallExpr) error {
	args := make([]any, 0, len(call.Args))
	for i, arg := range call.Args {
		switch arg := arg.(type) {
		case *ast.BasicLit:
			val, err := e.evalValue(arg)
			if err != nil {
				return fmt.Errorf("failed to eval argument[%d]: %w", i, err) // TODO: line number
			}
			args = append(args, val)
		case *ast.BinaryExpr:
			val, err := e.evalBinaryExpr(arg.Op, arg.X, arg.Y)
			if err != nil {
				return err
			}
			args = append(args, val)

		default:
			return fmt.Errorf("unsupported argument[%d] type: %T", i, arg) // TODO: line number
		}
	}
	fmt.Println(args...)
	return nil
}

func (e *evaluator) evalBinaryExpr(op token.Token, x, y ast.Expr) (string, error) {
	xval, err := e.evalValue(x)
	if err != nil {
		return "", err
	}
	yval, err := e.evalValue(y)
	if err != nil {
		return "", err
	}

	switch op {
	case token.ADD:
		return xval + yval, nil
	default:
		return "", fmt.Errorf("unsupported operator: %v", op)
	}
}

func (e *evaluator) evalValue(expr ast.Expr) (string, error) { // TODO: return any
	switch expr := expr.(type) {
	case *ast.BasicLit:
		return expr.Value[1 : len(expr.Value)-1], nil // TODO: fix
	case *ast.BinaryExpr:
		val, err := e.evalBinaryExpr(expr.Op, expr.X, expr.Y)
		if err != nil {
			return "", err
		}
		return val, nil
	default:
		return "-", fmt.Errorf("unsupported value type: %T", expr)
	}
}
