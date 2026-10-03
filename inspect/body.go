package inspect

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"reflect"
)

// Node is a read-only syntax view, not a typed or resolved expression.
// Role and Index identify the field and slice offset in its parent AST node.
// Owner anchors file imports and source positions, including inside literals.
// Type is present only at explicit type positions; local name resolution and
// expression type inference remain the consumer's responsibility.
type Node struct {
	Kind  string
	Text  string
	Pos   string
	Role  string
	Index int // -1 for a singular child
	Token string
	Owner *Decl
	Type  *TypeExpr
	node  ast.Node
}

// BodyOf exposes a source function or method without materializing its value.
// A bodyless source declaration returns nil; host and non-function views fail.
func BodyOf(s *Decl) (*Node, error) {
	if s == nil || s.decl == nil || s.decl.Func == nil {
		return nil, fmt.Errorf("inspect.Body: expected a source function or method")
	}
	if s.decl.Func.Body == nil {
		return nil, nil
	}
	return syntaxNode(s.decl.Func.Body, s, "Body", -1, false), nil
}

func syntaxNode(n ast.Node, owner *Decl, role string, index int, typePosition bool) *Node {
	v := &Node{Kind: reflect.TypeOf(n).Elem().Name(), Role: role, Index: index, Owner: owner, node: n}
	fset := token.NewFileSet()
	if owner.Package != nil && owner.Package.Fset != nil {
		fset = owner.Package.Fset
		v.Pos = fset.Position(n.Pos()).String()
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err == nil {
		v.Text = buf.String()
	}
	switch x := n.(type) {
	case *ast.CallExpr:
		if x.Ellipsis.IsValid() {
			v.Token = "..."
		}
	case *ast.BasicLit:
		v.Token = x.Kind.String()
	case *ast.AssignStmt:
		v.Token = x.Tok.String()
	case *ast.BinaryExpr:
		v.Token = x.Op.String()
	case *ast.UnaryExpr:
		v.Token = x.Op.String()
	case *ast.IncDecStmt:
		v.Token = x.Tok.String()
	case *ast.BranchStmt:
		v.Token = x.Tok.String()
	case *ast.GenDecl:
		v.Token = x.Tok.String()
	}
	if typePosition {
		if e, ok := n.(ast.Expr); ok {
			v.Type = NewTypeExpr(e, owner.file, owner.Package)
		}
	}
	return v
}

// ChildNodes returns direct syntax children in AST field order. Comments and
// semantic back-links (Ident.Obj, File.Scope) are intentionally excluded.
// Views are built on demand, so requesting a body does not wrap its whole tree.
func (n *Node) ChildNodes() []*Node {
	if n == nil || n.node == nil {
		return nil
	}
	rv := reflect.ValueOf(n.node).Elem()
	rt := rv.Type()
	var out []*Node
	add := func(v reflect.Value, role string, index int) {
		if !v.CanInterface() {
			return
		}
		child, ok := v.Interface().(ast.Node)
		if !ok || child == nil || (v.Kind() == reflect.Pointer && v.IsNil()) {
			return
		}
		switch child.(type) {
		case *ast.Comment, *ast.CommentGroup:
			return
		}
		// Interface fields may carry a typed nil pointer.
		cv := reflect.ValueOf(child)
		if cv.Kind() == reflect.Pointer && cv.IsNil() {
			return
		}
		typed := role == "Type"
		switch n.node.(type) {
		case *ast.ArrayType:
			typed = role == "Elt"
		case *ast.MapType:
			typed = role == "Key" || role == "Value"
		case *ast.ChanType:
			typed = role == "Value"
		case *ast.Ellipsis:
			typed = role == "Elt"
		}
		// Pointer/parenthesis children inherit an explicit type context.
		if n.Type != nil && (n.Kind == "StarExpr" || n.Kind == "ParenExpr") {
			typed = true
		}
		out = append(out, syntaxNode(child, n.Owner, role, index, typed))
	}
	for i := 0; i < rv.NumField(); i++ {
		field, role := rv.Field(i), rt.Field(i).Name
		if field.Kind() == reflect.Slice {
			for j := 0; j < field.Len(); j++ {
				add(field.Index(j), role, j)
			}
		} else {
			add(field, role, -1)
		}
	}
	return out
}
