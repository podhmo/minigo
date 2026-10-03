package inspect

// node.go — the experimental body view (TODO.md "script-side traversal
// of function bodies"): a Node handle over any syntax tree node,
// mirroring TypeExpr's "spelling + declaring context" shape. Bodies
// enter through BodyOf (a func decl's BlockStmt); ChildrenOf walks
// statements and expressions uniformly; AsExprOf re-wraps an
// expression node as a TypeExpr so SymbolID/Resolve/Sub apply.

import (
	"bytes"
	"go/ast"
	"go/printer"
	"go/token"
	"reflect"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// Node is a handle over one syntax tree node inside a declaration
// body: the node itself, a kind/operator/name summary for dispatch,
// and the file/package context needed to resolve the names it
// mentions.
type Node struct {
	Kind  string   // ast node type name: "CallExpr", "AssignStmt", ...
	Role  string   // slot this node fills in its parent: "fun", "args", "lhs", "type", "x", ...
	Text  string   // printer spelling of the whole node
	Pos   string   // "file.go:12:3"
	Op    string   // operator/token: ":=", "+", "break", "var", "STRING", ...
	Name  string   // Ident.Name, SelectorExpr.Sel.Name, BranchStmt label
	Value string   // BasicLit.Value
	Names []string // ValueSpec/TypeSpec/Field declared names

	node ast.Node
	file *syntax.File
	pkg  *runtime.Package
}

// NewNode builds a node view in a declaring file's context.
func NewNode(n ast.Node, f *syntax.File, p *runtime.Package) *Node {
	if n == nil {
		return nil
	}
	v := &Node{node: n, file: f, pkg: p}
	if t := reflect.TypeOf(n); t != nil && t.Kind() == reflect.Pointer {
		v.Kind = t.Elem().Name()
	}
	if p != nil && p.Fset != nil {
		v.Pos = p.Fset.Position(n.Pos()).String()
	}
	fset := token.NewFileSet()
	if p != nil && p.Fset != nil {
		fset = p.Fset
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err == nil {
		v.Text = buf.String()
	}
	switch x := n.(type) {
	case *ast.Ident:
		v.Name = x.Name
	case *ast.SelectorExpr:
		v.Name = x.Sel.Name
	case *ast.BasicLit:
		v.Op = x.Kind.String()
		v.Value = x.Value
	case *ast.AssignStmt:
		v.Op = x.Tok.String()
	case *ast.BinaryExpr:
		v.Op = x.Op.String()
	case *ast.UnaryExpr:
		v.Op = x.Op.String()
	case *ast.IncDecStmt:
		v.Op = x.Tok.String()
	case *ast.BranchStmt:
		v.Op = x.Tok.String()
		if x.Label != nil {
			v.Name = x.Label.Name
		}
	case *ast.LabeledStmt:
		v.Name = x.Label.Name
	case *ast.SendStmt:
		v.Op = "<-"
	case *ast.GenDecl:
		v.Op = x.Tok.String()
	case *ast.RangeStmt:
		v.Op = x.Tok.String()
	case *ast.ChanType:
		switch x.Dir {
		case ast.SEND:
			v.Op = "chan<-"
		case ast.RECV:
			v.Op = "<-chan"
		default:
			v.Op = "chan"
		}
	case *ast.ValueSpec:
		for _, id := range x.Names {
			v.Names = append(v.Names, id.Name)
		}
	case *ast.TypeSpec:
		v.Names = []string{x.Name.Name}
	case *ast.Field:
		for _, id := range x.Names {
			v.Names = append(v.Names, id.Name)
		}
	case *ast.CaseClause:
		if x.List == nil {
			v.Op = "default"
		} else {
			v.Op = "case"
		}
	case *ast.CommClause:
		if x.Comm == nil {
			v.Op = "default"
		} else {
			v.Op = "case"
		}
	}
	return v
}

// AST exposes the underlying ast.Node — engine-only, out of the FFI.
func (v *Node) AST() ast.Node { return v.node }

// File exposes the declaring file — engine-only.
func (v *Node) File() *syntax.File { return v.file }

// Pkg exposes the owning package — engine-only.
func (v *Node) Pkg() *runtime.Package { return v.pkg }

// BodyOf returns the body view of a func/method decl — its BlockStmt.
// Decls without a body (vars, consts, types, host pseudo-decls,
// bodiless signatures) report nil.
func BodyOf(s *Decl) *Node {
	if s == nil || s.decl == nil || s.decl.Func == nil || s.decl.Func.Body == nil {
		return nil
	}
	return NewNode(s.decl.Func.Body, s.file, s.Package)
}

// AsExprOf re-wraps an expression node as a TypeExpr in the same
// context, so SymbolID/Resolve/Sub/UnWrap apply. Non-expression nodes
// report nil.
func AsExprOf(v *Node) *TypeExpr {
	if v == nil {
		return nil
	}
	e, ok := v.node.(ast.Expr)
	if !ok {
		return nil
	}
	return NewTypeExpr(e, v.file, v.pkg)
}

// ChildrenOf lists a node's children in source order: statements and
// expressions are uniformly re-wrapped as Nodes. Each child carries a
// Role naming the slot it fills in the parent ("fun", "args", "lhs",
// "type", "x", ...) — position alone is ambiguous in specs that mix
// optional fields (a ValueSpec's sole trailing child can be its Type
// or its Value).
func ChildrenOf(v *Node) []*Node {
	if v == nil {
		return nil
	}
	var out []*Node
	wrap := func(n ast.Node, role string) {
		if n != nil && !reflect.ValueOf(n).IsNil() {
			c := NewNode(n, v.file, v.pkg)
			c.Role = role
			out = append(out, c)
		}
	}
	switch x := v.node.(type) {
	// ---- declarations ----
	case *ast.GenDecl:
		addAll(wrap, "spec", x.Specs)
	case *ast.ValueSpec:
		addAll(wrap, "names", x.Names)
		wrap(x.Type, "type")
		addAll(wrap, "values", x.Values)
	case *ast.TypeSpec:
		wrap(x.Name, "name")
		wrap(x.TypeParams, "typeparams")
		wrap(x.Type, "type")
	case *ast.ImportSpec:
		wrap(x.Name, "name")
		wrap(x.Path, "path")
	case *ast.Field:
		addAll(wrap, "names", x.Names)
		wrap(x.Type, "type")
		wrap(x.Tag, "tag")
	case *ast.FieldList:
		addAll(wrap, "field", x.List)
	case *ast.FuncDecl:
		wrap(x.Recv, "recv")
		wrap(x.Name, "name")
		wrap(x.Type, "type")
		wrap(x.Body, "body")
	// ---- statements ----
	case *ast.BlockStmt:
		addAll(wrap, "stmt", x.List)
	case *ast.ExprStmt:
		wrap(x.X, "x")
	case *ast.AssignStmt:
		addAll(wrap, "lhs", x.Lhs)
		addAll(wrap, "rhs", x.Rhs)
	case *ast.DeclStmt:
		wrap(x.Decl, "decl")
	case *ast.ReturnStmt:
		addAll(wrap, "result", x.Results)
	case *ast.IfStmt:
		wrap(x.Init, "init")
		wrap(x.Cond, "cond")
		wrap(x.Body, "body")
		wrap(x.Else, "else")
	case *ast.ForStmt:
		wrap(x.Init, "init")
		wrap(x.Cond, "cond")
		wrap(x.Post, "post")
		wrap(x.Body, "body")
	case *ast.RangeStmt:
		wrap(x.Key, "key")
		wrap(x.Value, "value")
		wrap(x.X, "x")
		wrap(x.Body, "body")
	case *ast.IncDecStmt:
		wrap(x.X, "x")
	case *ast.GoStmt:
		wrap(x.Call, "call")
	case *ast.DeferStmt:
		wrap(x.Call, "call")
	case *ast.SendStmt:
		wrap(x.Chan, "chan")
		wrap(x.Value, "value")
	case *ast.LabeledStmt:
		wrap(x.Stmt, "stmt")
	case *ast.SwitchStmt:
		wrap(x.Init, "init")
		wrap(x.Tag, "tag")
		wrap(x.Body, "body")
	case *ast.TypeSwitchStmt:
		wrap(x.Init, "init")
		wrap(x.Assign, "assign")
		wrap(x.Body, "body")
	case *ast.SelectStmt:
		wrap(x.Body, "body")
	case *ast.CaseClause:
		addAll(wrap, "case", x.List)
		addAll(wrap, "stmt", x.Body)
	case *ast.CommClause:
		wrap(x.Comm, "comm")
		addAll(wrap, "stmt", x.Body)
	case *ast.BadStmt, *ast.EmptyStmt:
		// leaves
	// ---- expressions ----
	case *ast.CallExpr:
		wrap(x.Fun, "fun")
		addAll(wrap, "args", x.Args)
	case *ast.SelectorExpr:
		wrap(x.X, "x")
	case *ast.IndexExpr:
		wrap(x.X, "x")
		wrap(x.Index, "index")
	case *ast.IndexListExpr:
		wrap(x.X, "x")
		addAll(wrap, "index", x.Indices)
	case *ast.SliceExpr:
		wrap(x.X, "x")
		wrap(x.Low, "low")
		wrap(x.High, "high")
		wrap(x.Max, "max")
	case *ast.UnaryExpr:
		wrap(x.X, "x")
	case *ast.BinaryExpr:
		wrap(x.X, "x")
		wrap(x.Y, "y")
	case *ast.ParenExpr:
		wrap(x.X, "x")
	case *ast.StarExpr:
		wrap(x.X, "x")
	case *ast.KeyValueExpr:
		wrap(x.Key, "key")
		wrap(x.Value, "value")
	case *ast.CompositeLit:
		wrap(x.Type, "type")
		addAll(wrap, "elt", x.Elts)
	case *ast.TypeAssertExpr:
		wrap(x.X, "x")
		wrap(x.Type, "type")
	case *ast.FuncLit:
		wrap(x.Type, "type")
		wrap(x.Body, "body")
	case *ast.FuncType:
		wrap(x.TypeParams, "typeparams")
		wrap(x.Params, "params")
		wrap(x.Results, "results")
	case *ast.ArrayType:
		wrap(x.Len, "len")
		wrap(x.Elt, "elt")
	case *ast.MapType:
		wrap(x.Key, "key")
		wrap(x.Value, "value")
	case *ast.ChanType:
		wrap(x.Value, "value")
	case *ast.StructType:
		wrap(x.Fields, "fields")
	case *ast.InterfaceType:
		wrap(x.Methods, "methods")
	case *ast.Ellipsis:
		wrap(x.Elt, "elt")
	case *ast.Ident, *ast.BasicLit, *ast.BadExpr:
		// leaves
	}
	return out
}

// addAll wraps each element as a child node under one role (generic so
// the concrete []ast.Spec/[]ast.Stmt/[]ast.Expr slices all fit the
// same walk).
func addAll[T ast.Node](wrap func(ast.Node, string), role string, xs []T) {
	for _, x := range xs {
		wrap(x, role)
	}
}
