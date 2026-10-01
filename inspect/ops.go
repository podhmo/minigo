// ops.go — an experimental compiled-body view ("special VM code").
//
// Rather than mirroring the whole AST to scripts, a function body is
// compiled into a flat op list of a handful of kinds — ref, lit, sel,
// call, bind, ret, comp, func, index, expr — so a script can track
// which values flow into a call as arguments and back out as results.
// Each op defines a register (its index in the list); refs/sels/calls
// reference earlier registers, which is what makes argument and return
// tracking cheap. Each op also keeps the originating ast.Node
// (unexported) so SymbolID/AsExpr/Lookup keep working on it.

package inspect

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Op is one instruction of a compiled function body.
type Op struct {
	Kind  string         // ref|lit|sel|call|bind|ret|comp|func|index|expr
	Pos   string         // "file.go:12:3"
	Dst   int            // register this op defines (its index in the list)
	Name  string         // ref: ident; sel: member name
	Names []string       // bind: bound names
	Value string         // lit: literal spelling
	Tok   string         // lit: token kind; bind: "param"|"var"|":="|"="|"range"
	X     int            // sel/index: base register (-1 when none)
	Fun   int            // call: callee register
	Args  *runtime.Slice // call: argument registers (int64s)
	Srcs  *runtime.Slice // bind/ret: source registers
	Text  string         // printer spelling of the backing node

	node ast.Node
	file *syntax.File
	pkg  *runtime.Package
	body []*Op // Kind=="func": the literal's own compiled ops
}

// AST exposes the backing node. It is go/ast-typed, so from a script
// the opaque value is only useful through the inspect functions.
func (o *Op) AST() ast.Node         { return o.node }
func (o *Op) File() *syntax.File    { return o.file }
func (o *Op) Pkg() *runtime.Package { return o.pkg }

func intSlice(xs []int) *runtime.Slice {
	el := make([]runtime.Value, len(xs))
	for i, x := range xs {
		el[i] = int64(x)
	}
	return &runtime.Slice{Elems: el}
}

type compiler struct {
	ops  []*Op
	file *syntax.File
	pkg  *runtime.Package
}

func (c *compiler) emit(kind string, n ast.Node) *Op {
	op := &Op{Kind: kind, Dst: len(c.ops), X: -1, Fun: -1, node: n, file: c.file, pkg: c.pkg,
		Args: intSlice(nil), Srcs: intSlice(nil)}
	if n != nil {
		op.Text = spell(n, c.pkg)
		if c.pkg != nil && c.pkg.Fset != nil {
			op.Pos = c.pkg.Fset.Position(n.Pos()).String()
		}
	}
	c.ops = append(c.ops, op)
	return op
}

// OpsOf compiles a function body into its op list. x is a *Decl
// (func/method) or a *Op of Kind "func" (a literal's body — param
// binds lead it, just like a decl's).
func OpsOf(x any) ([]*Op, error) {
	switch v := x.(type) {
	case *Decl:
		if v.decl == nil || v.decl.Func == nil {
			return nil, fmt.Errorf("inspect.Ops: %s has no body", v.Kind)
		}
		c := &compiler{file: v.file, pkg: v.Package}
		c.params(v.decl.Func.Type.Params)
		c.block(v.decl.Func.Body)
		return c.ops, nil
	case *Op:
		if v.Kind != "func" {
			return nil, fmt.Errorf("inspect.Ops: op kind %q has no body", v.Kind)
		}
		return v.body, nil
	default:
		return nil, fmt.Errorf("inspect.Ops: %T has no body", x)
	}
}

func (c *compiler) valueSpec(vs *ast.ValueSpec) {
	var srcs []int
	for _, e := range vs.Values {
		srcs = append(srcs, c.expr(e))
	}
	op := c.emit("bind", vs)
	op.Tok = "var"
	if vs.Type != nil {
		op.node = vs.Type // AsExpr reads the declared type
	}
	for _, n := range vs.Names {
		op.Names = append(op.Names, n.Name)
	}
	op.Srcs = intSlice(srcs)
}

func (c *compiler) params(fl *ast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		op := c.emit("bind", f.Type)
		op.Tok = "param"
		for _, n := range f.Names {
			op.Names = append(op.Names, n.Name)
		}
	}
}

func (c *compiler) block(b *ast.BlockStmt) {
	if b == nil {
		return
	}
	for _, s := range b.List {
		c.stmt(s)
	}
}

func (c *compiler) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.AssignStmt:
		var srcs []int
		for _, e := range s.Rhs {
			srcs = append(srcs, c.expr(e))
		}
		op := c.emit("bind", s)
		op.Tok = s.Tok.String()
		for _, l := range s.Lhs {
			if id, ok := l.(*ast.Ident); ok {
				op.Names = append(op.Names, id.Name)
			}
		}
		op.Srcs = intSlice(srcs)
	case *ast.DeclStmt:
		if gd, ok := s.Decl.(*ast.GenDecl); ok {
			for _, sp := range gd.Specs {
				if vs, ok := sp.(*ast.ValueSpec); ok {
					c.valueSpec(vs)
				}
			}
		}
	case *ast.ExprStmt:
		c.expr(s.X)
	case *ast.ReturnStmt:
		var srcs []int
		for _, e := range s.Results {
			srcs = append(srcs, c.expr(e))
		}
		op := c.emit("ret", s)
		op.Srcs = intSlice(srcs)
	case *ast.IfStmt:
		if s.Init != nil {
			c.stmt(s.Init)
		}
		if s.Cond != nil {
			c.expr(s.Cond)
		}
		c.block(s.Body)
		if s.Else != nil {
			switch e := s.Else.(type) {
			case *ast.BlockStmt:
				c.block(e)
			default:
				c.stmt(e)
			}
		}
	case *ast.ForStmt:
		if s.Init != nil {
			c.stmt(s.Init)
		}
		if s.Cond != nil {
			c.expr(s.Cond)
		}
		if s.Post != nil {
			c.stmt(s.Post)
		}
		c.block(s.Body)
	case *ast.RangeStmt:
		x := c.expr(s.X)
		op := c.emit("bind", s)
		op.Tok = "range"
		for _, e := range []ast.Expr{s.Key, s.Value} {
			if id, ok := e.(*ast.Ident); ok {
				op.Names = append(op.Names, id.Name)
			}
		}
		op.Srcs = intSlice([]int{x})
		c.block(s.Body)
	case *ast.SwitchStmt:
		if s.Init != nil {
			c.stmt(s.Init)
		}
		if s.Tag != nil {
			c.expr(s.Tag)
		}
		if s.Body != nil {
			for _, st := range s.Body.List {
				if cc, ok := st.(*ast.CaseClause); ok {
					for _, e := range cc.List {
						c.expr(e)
					}
					for _, bs := range cc.Body {
						c.stmt(bs)
					}
				}
			}
		}
	case *ast.TypeSwitchStmt:
		if s.Init != nil {
			c.stmt(s.Init)
		}
		if s.Assign != nil {
			c.stmt(s.Assign)
		}
		if s.Body != nil {
			for _, st := range s.Body.List {
				if cc, ok := st.(*ast.CaseClause); ok {
					for _, bs := range cc.Body {
						c.stmt(bs)
					}
				}
			}
		}
	case *ast.SelectStmt:
		if s.Body != nil {
			for _, st := range s.Body.List {
				if cc, ok := st.(*ast.CommClause); ok {
					if cc.Comm != nil {
						c.stmt(cc.Comm)
					}
					for _, bs := range cc.Body {
						c.stmt(bs)
					}
				}
			}
		}
	case *ast.BlockStmt:
		c.block(s)
	case *ast.LabeledStmt:
		c.stmt(s.Stmt)
	case *ast.IncDecStmt:
		c.expr(s.X)
	case *ast.SendStmt:
		c.expr(s.Chan)
		c.expr(s.Value)
	case *ast.GoStmt:
		c.expr(s.Call)
	case *ast.DeferStmt:
		c.expr(s.Call)
	}
}

// expr compiles e and returns its register. Unknown expressions
// become a generic "expr" op so the result is still referenceable.
func (c *compiler) expr(e ast.Expr) int {
	switch e := e.(type) {
	case *ast.Ident:
		op := c.emit("ref", e)
		op.Name = e.Name
		return op.Dst
	case *ast.BasicLit:
		op := c.emit("lit", e)
		op.Value = e.Value
		op.Tok = e.Kind.String()
		return op.Dst
	case *ast.SelectorExpr:
		op := c.emit("sel", e)
		op.X = c.expr(e.X)
		op.Name = e.Sel.Name
		return op.Dst
	case *ast.CallExpr:
		op := c.emit("call", e.Fun)
		op.Fun = c.expr(e.Fun)
		var args []int
		for _, a := range e.Args {
			args = append(args, c.expr(a))
		}
		op.Args = intSlice(args)
		return op.Dst
	case *ast.FuncLit:
		op := c.emit("func", e)
		sub := &compiler{file: c.file, pkg: c.pkg}
		sub.params(e.Type.Params)
		sub.block(e.Body)
		op.body = sub.ops
		return op.Dst
	case *ast.CompositeLit:
		op := c.emit("comp", e.Type)
		return op.Dst
	case *ast.ParenExpr:
		return c.expr(e.X)
	case *ast.StarExpr:
		return c.expr(e.X)
	case *ast.UnaryExpr:
		return c.expr(e.X)
	case *ast.TypeAssertExpr:
		return c.expr(e.X)
	case *ast.IndexExpr:
		op := c.emit("index", e)
		op.X = c.expr(e.X)
		op.Srcs = intSlice([]int{c.expr(e.Index)})
		return op.Dst
	case *ast.SliceExpr:
		op := c.emit("index", e)
		op.X = c.expr(e.X)
		var xs []int
		for _, e2 := range []ast.Expr{e.Low, e.High, e.Max} {
			if e2 != nil {
				xs = append(xs, c.expr(e2))
			}
		}
		op.Srcs = intSlice(xs)
		return op.Dst
	case *ast.IndexListExpr:
		op := c.emit("index", e)
		op.X = c.expr(e.X)
		var xs []int
		for _, e2 := range e.Indices {
			xs = append(xs, c.expr(e2))
		}
		op.Srcs = intSlice(xs)
		return op.Dst
	case *ast.BinaryExpr:
		op := c.emit("expr", e)
		op.Srcs = intSlice([]int{c.expr(e.X), c.expr(e.Y)})
		return op.Dst
	default:
		return c.emit("expr", e).Dst
	}
}

// spell renders a node (same printing as Node.Text).
func spell(n ast.Node, p *runtime.Package) string {
	fset := token.NewFileSet()
	if p != nil && p.Fset != nil {
		fset = p.Fset
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}
