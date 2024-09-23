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

func New(fset *token.FileSet, options ...func(*Interpreter)) *Interpreter {
	stdout := os.Stdout
	stderr := os.Stderr

	var i *Interpreter

	packages := map[string]*Package{
		"fmt": {Name: "fmt", Path: "fmt", Decls: map[string]reflect.Value{
			"Println": reflect.ValueOf(func(args ...interface{}) (int, error) {
				return fmt.Fprintln(i.evaluator.stdout, args...)
			})},
		},
		"strings": {Name: "strings", Path: "strings", Decls: map[string]reflect.Value{
			"ToUpper": reflect.ValueOf(strings.ToUpper),
		}},
	}
	packages["github.com/podhmo/minigo/stdlib/strings"] = packages["strings"]
	packages["github.com/podhmo/minigo/stdlib/fmt"] = packages["fmt"]

	scope := &scope{frames: []map[string]reflect.Value{{
		"true":  reflect.ValueOf(true),
		"false": reflect.ValueOf(false),
		"println": reflect.ValueOf(func(args ...interface{}) (int, error) {
			return fmt.Fprintln(i.evaluator.stdout, args...)
		}),
	}}}
	i = &Interpreter{
		fset: fset,
		evaluator: &evaluator{stdout: stdout, stderr: stderr,
			packages: packages,
			scope:    scope,
		},
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
	fset      *token.FileSet
	evaluator *evaluator
}

func (app *Interpreter) RunFile(ctx context.Context, node *ast.File, entrypoint string) error {
	fset := app.fset
	filename := fset.Position(node.Pos()).Filename

	pkg, ok := app.evaluator.packages["main"]
	if !ok {
		pkg = &Package{Name: "main", Path: "main", Decls: map[string]reflect.Value{}, Files: map[string]*File{}}
		app.evaluator.packages[pkg.Path] = pkg
	}
	file, ok := pkg.Files[filename]
	if !ok {
		file = &File{Name: filename, Node: node, Imports: map[string]string{}}
		pkg.Files[filename] = file
		for _, im := range node.Imports {
			name := ""
			if im.Name != nil {
				name = im.Name.Name
			}
			path := strings.Trim(im.Path.Value, `"`)
			if name == "" { // heuristic
				parts := strings.Split(path, "/")
				name = parts[len(parts)-1]
			}
			file.Imports[name] = path
		}
	}

	app.evaluator.history = append(app.evaluator.history, file) // TODO: line number
	defer func() { app.evaluator.history = app.evaluator.history[:len(app.evaluator.history)-1] }()

	for _, decl := range node.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Name.Name == entrypoint {
				return app.runFunc(ctx, fn)
			}
		}
	}
	return fmt.Errorf("entrypoint func %s() is not found", entrypoint)
}

func (app *Interpreter) runFunc(ctx context.Context, fn *ast.FuncDecl) error {
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

	scope    *scope
	packages map[string]*Package // path -> Package
	history  []*File
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
		if rfn, ok := e.scope.Get(ident.Name); ok {
			out := rfn.Call(args)
			if len(out) == 0 {
				return zero, nil
			}
			return out[0], nil // TODO: multiple return values
		} else {
			return zero, fmt.Errorf("unsupported function: %s", ident.Name)
		}
	case *ast.SelectorExpr:
		sel := fun
		// only support fmt.Println() and strings.ToUpper()
		if ident, ok := sel.X.(*ast.Ident); ok {
			// TODO: scan packages if needed
			pkgPath := e.history[len(e.history)-1].Imports[ident.Name]
			if pkg, ok := e.packages[pkgPath]; ok {
				if rfn, ok := pkg.Decls[sel.Sel.Name]; ok {
					out := rfn.Call(args)
					if len(out) == 0 {
						return zero, nil
					}
					return out[0], nil // TODO: multiple return values
				} else {
					return zero, fmt.Errorf("unsupported function: %s.%s", ident.Name, sel.Sel.Name)
				}
			}
		}
		return zero, fmt.Errorf("unsupported function:: %s.%s", sel.X, sel.Sel.Name)
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

type Package struct {
	Name string
	Path string

	Decls map[string]reflect.Value // ordered?
	Files map[string]*File
}

type File struct {
	Name    string
	Node    *ast.File
	Imports map[string]string // name -> pathe
}
