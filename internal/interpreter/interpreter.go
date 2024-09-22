package interpreter

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
)

func New(fset *token.FileSet, entryPoint string, options ...func(*Interpreter)) *Interpreter {
	stdout := os.Stdout
	stderr := os.Stderr
	i := &Interpreter{
		fset:       fset,
		entryPoint: entryPoint,
		evaluator: &evaluator{stdout: stdout, stderr: stderr, scope: &scope{frames: []map[string]reflect.Value{{
			"true":  reflect.ValueOf(true),
			"false": reflect.ValueOf(false),
		}}}},
	}
	for _, opt := range options {
		opt(i)
	}
	return i
}
func WithStdout(w io.Writer) func(*Interpreter) {
	return func(app *Interpreter) {
		app.evaluator.stdout = w
	}
}
func WithStderr(w io.Writer) func(*Interpreter) {
	return func(app *Interpreter) {
		app.evaluator.stderr = w
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
	frames []map[string]reflect.Value
}

func (s *scope) Push() {
	s.frames = append(s.frames, map[string]reflect.Value{})
}
func (s *scope) Pop() {
	s.frames = s.frames[:len(s.frames)-1]
}

func (s *scope) Set(name string, value reflect.Value) {
	s.frames[len(s.frames)-1][name] = value
}

func (s *scope) Get(name string) (reflect.Value, bool) {
	for n := len(s.frames) - 1; n >= 0; n-- {
		if val, ok := s.frames[n][name]; ok {
			return val, true
		}
	}
	return zero, false
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

func (e *evaluator) EvalExpr(ctx context.Context, expr ast.Expr) (reflect.Value, error) {
	switch expr := expr.(type) {
	case *ast.BasicLit:
		switch expr.Kind {
		case token.INT:
			v, err := strconv.Atoi(expr.Value)
			if err != nil {
				return zero, fmt.Errorf("failed to convert int: %w", err)
			}
			return reflect.ValueOf(v), nil
		case token.FLOAT:
			v, err := strconv.ParseFloat(expr.Value, 64)
			if err != nil {
				return zero, fmt.Errorf("failed to convert float: %w", err)
			}
			return reflect.ValueOf(v), nil
		case token.STRING:
			return reflect.ValueOf(expr.Value[1 : len(expr.Value)-1]), nil
		default:
			return zero, fmt.Errorf("unsupported basic lit kind: %v, value=%v", expr.Kind, expr.Value)
		}
	case *ast.Ident:
		val, ok := e.scope.Get(expr.Name)
		if !ok {
			return zero, fmt.Errorf("undefined variable: %s", expr.Name)
		}
		return val, nil
	case *ast.BinaryExpr:
		return e.evalBinaryExpr(ctx, expr)
	case *ast.CallExpr:
		return e.evalCallExpr(ctx, expr)
	default:
		return zero, fmt.Errorf("unsupported expr type: %T", expr)
	}
}

func (e *evaluator) evalCallExpr(ctx context.Context, expr *ast.CallExpr) (reflect.Value, error) {
	args := make([]reflect.Value, 0, len(expr.Args))
	for i, arg := range expr.Args {
		val, err := e.EvalExpr(ctx, arg)
		if err != nil {
			return zero, fmt.Errorf("failed to eval argument[%d]: %w", i, err)
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
			rfn := reflect.ValueOf(fmt.Fprintln)
			rfn.Call(append([]reflect.Value{reflect.ValueOf(e.stdout)}, args...))
			return zero, nil
		} else {
			return zero, fmt.Errorf("unsupported function: %s", ident.Name)
		}
	case *ast.SelectorExpr:
		sel := fun
		// only support fmt.Println() and strings.ToUpper()
		if ident, ok := sel.X.(*ast.Ident); ok {
			if ident.Name == "fmt" && sel.Sel.Name == "Println" {
				rfn := reflect.ValueOf(fmt.Fprintln)
				rfn.Call(append([]reflect.Value{reflect.ValueOf(e.stdout)}, args...))
				return zero, nil
			} else if ident.Name == "strings" && sel.Sel.Name == "ToUpper" {
				rfn := reflect.ValueOf(strings.ToUpper)
				return rfn.Call(args)[0], nil
			} else {
				return zero, fmt.Errorf("unsupported function: %s.%s", sel.X, sel.Sel.Name)
			}
		} else {
			return zero, fmt.Errorf("unsupported function:: %s.%s", sel.X, sel.Sel.Name)
		}
	default:
		return zero, fmt.Errorf("unsupported function: %T", expr.Fun)
	}
}

func (e *evaluator) evalBinaryExpr(ctx context.Context, expr *ast.BinaryExpr) (reflect.Value, error) {
	x, err := e.EvalExpr(ctx, expr.X)
	if err != nil {
		return zero, fmt.Errorf("failed to eval binary expr lhs: %w", err)
	}
	y, err := e.EvalExpr(ctx, expr.Y)
	if err != nil {
		return zero, fmt.Errorf("failed to eval binary expr rhs: %w", err)
	}

	// only support ADD, LOR, LAND
	if !x.IsValid() || !y.IsValid() || x.Kind() != y.Kind() {
		return zero, fmt.Errorf("invalid reflect.Value: %v, %v", x, y)
	}
	switch expr.Op {
	case token.ADD:
		switch x.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return reflect.ValueOf(x.Int() + y.Int()), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return reflect.ValueOf(x.Uint() + y.Uint()), nil
		case reflect.Float64, reflect.Float32:
			return reflect.ValueOf(x.Float() + y.Float()), nil
		case reflect.String:
			return reflect.ValueOf(x.String() + y.String()), nil
		default:
			return zero, fmt.Errorf("unsupported types: %s, %s", x.Kind(), y.Kind())
		}
	case token.LOR:
		switch x.Kind() {
		case reflect.Bool:
			return reflect.ValueOf(x.Bool() || y.Bool()), nil
		default:
			return zero, fmt.Errorf("unsupported types: %s, %s", x.Kind(), y.Kind())
		}
	case token.LAND:
		switch x.Kind() {
		case reflect.Bool:
			return reflect.ValueOf(x.Bool() && y.Bool()), nil
		default:
			return zero, fmt.Errorf("unsupported types: %s, %s", x.Kind(), y.Kind())
		}
	default:
		return zero, fmt.Errorf("unsupported operator: %v", expr.Op)
	}
}

var zero = reflect.Value{}
