package compile

import (
	"go/ast"

	"github.com/podhmo/minigo/index"
)

// P4-2a slot classification: every OpLocalRef emit site records whether
// the ref can outlive the frame — the ref IS a value (`&x`), a ref base
// survives inside a produced ref (`&x.f`), or a select binds the cell
// through a pointer receiver. Stores and reads consume their ref
// transiently and stay plain. A receiver whose member resolution is
// unknown boxes conservatively: boxing a plain slot is wasteful, but a
// missed box breaks pointer-receiver writes once plain slots lose
// their cells (P4-2b).

// boxReceiver marks the root local of a select's operand when the
// member may bind the cell: a pointer-receiver method call `x.M()` or
// method value `x.M`, where M's receiver is *T and x has type T. Field
// reads, value-receiver selects, interface dispatches and detached
// copies (map elements) consume the ref transiently.
func (c *compiler) boxReceiver(base ast.Expr, sel string) {
	if !c.selectNeedsCell(base, sel) {
		return
	}
	if id := rootIdent(base); id != nil {
		c.fs.boxName(id.Name)
	}
}

// rootIdent returns the identifier at the bottom of a ref chain — the
// name whose storage any produced ref resolves through.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch t := ast.Unparen(e).(type) {
		case *ast.Ident:
			return t
		case *ast.SelectorExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.StarExpr:
			e = t.X
		default:
			return nil
		}
	}
}

// baseKind is the resolution status of a select operand's static type.
type baseKind int

const (
	baseUnknown baseKind = iota // unresolvable — box conservatively
	baseTyp                     // the operand's type expression
	baseIface                   // interface-typed: dynamic dispatch never binds the cell
	baseCopy                    // the operand is a detached copy (map element)
	baseNoCell                  // no local cell involved (global, type name)
)

// selectNeedsCell reports whether selecting sel on the ref operand of
// base can keep the operand's cell alive past the select.
func (c *compiler) selectNeedsCell(base ast.Expr, sel string) bool {
	te, kind := c.baseStaticTyp(base)
	switch kind {
	case baseNoCell, baseIface, baseCopy:
		return false
	case baseUnknown:
		return true
	}
	return c.typSelectNeedsCell(te, sel)
}

// baseStaticTyp resolves the static type expression of a ref operand:
// an ident's declared type, a select's field type (through struct
// decls), an index's element type, a deref's pointee. Unresolvable
// operands report baseUnknown.
func (c *compiler) baseStaticTyp(e ast.Expr) (ast.Expr, baseKind) {
	switch t := ast.Unparen(e).(type) {
	case *ast.Ident:
		b := c.fs.lookupBinding(t.Name)
		if b == nil {
			// not a local binding — a global var ref or a package
			// qualifier: no local cell to classify.
			return nil, baseNoCell
		}
		if b.ifaceVar {
			return nil, baseIface
		}
		if b.typeDecl {
			return nil, baseNoCell // a type name selects on the type itself
		}
		if b.declTyp == nil {
			return nil, baseUnknown
		}
		return b.declTyp, baseTyp
	case *ast.SelectorExpr:
		xt, kind := c.baseStaticTyp(t.X)
		if kind != baseTyp {
			return nil, kind
		}
		if ft := c.structFieldTyp(xt, t.Sel.Name); ft != nil {
			return ft, baseTyp
		}
		return nil, baseUnknown
	case *ast.IndexExpr:
		xt, kind := c.baseStaticTyp(t.X)
		if kind != baseTyp {
			return nil, kind
		}
		switch u := ast.Unparen(c.unfoldTypExpr(xt, 4)).(type) {
		case *ast.ArrayType:
			return u.Elt, baseTyp
		case *ast.MapType:
			// a map element is a detached copy — Go never writes a
			// pointer-receiver method call back through it, so the
			// container's cell is never bound.
			return nil, baseCopy
		}
		return nil, baseUnknown
	case *ast.StarExpr:
		xt, kind := c.baseStaticTyp(t.X)
		if kind != baseTyp {
			return nil, kind
		}
		if st, ok := ast.Unparen(c.unfoldTypExpr(xt, 4)).(*ast.StarExpr); ok {
			return st.X, baseTyp
		}
		return nil, baseUnknown
	}
	return nil, baseNoCell
}

// structFieldTyp returns the declared type expression of field sel on
// struct type te — dereferencing pointers, resolving named types, and
// naming embedded fields by their type name. It reports nil when te is
// not a resolvable struct or sel is not a direct field.
func (c *compiler) structFieldTyp(te ast.Expr, sel string) ast.Expr {
	st, ok := ast.Unparen(c.unfoldTypExpr(te, 4)).(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			// an embedded field's field name is its type name.
			if embeddedName(f.Type) == sel {
				return f.Type
			}
			continue
		}
		for _, n := range f.Names {
			if n.Name == sel {
				return f.Type
			}
		}
	}
	return nil
}

// embeddedName returns the field name of an embedded field's type
// expression — U for `U` and `*U`, pkg.U for `pkg.U`, F for `F[T]`.
func embeddedName(te ast.Expr) string {
	switch t := ast.Unparen(te).(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	}
	return ""
}

// typSelectNeedsCell classifies a select on an operand whose static
// type expression is te: a pointer-receiver method binds the cell;
// fields, value-receiver methods, interfaces and memberless types do
// not. Unresolvable receivers box.
func (c *compiler) typSelectNeedsCell(te ast.Expr, sel string) bool {
	t := ast.Unparen(te)
	switch t := t.(type) {
	case *ast.StarExpr:
		// the operand is already a pointer: the select binds the
		// pointer value, never the cell holding it.
		return false
	case *ast.Ident:
		if _, bound := c.binds[t.Name]; bound {
			return true // a type parameter's receiver is unknown
		}
		if predeclaredTypeNames[t.Name] {
			return false
		}
		if b := c.fs.lookupBinding(t.Name); b != nil && b.typeDecl {
			// a local `type` decl cannot carry methods: an interface
			// dispatches dynamically, a struct member is a field or a
			// promoted member of an embedded field.
			if b.ifaceT {
				return false
			}
			if c.structFieldTyp(t, sel) != nil {
				return false
			}
			_, isStruct := ast.Unparen(c.unfoldTypExpr(t, 4)).(*ast.StructType)
			return isStruct // a promoted member could be a ptr method
		}
		if tdi := c.typeDeclOf(t); tdi != nil {
			return indexSelectNeedsCell(tdi, sel)
		}
		return true // an unresolved type name — conservative
	case *ast.StructType, *ast.InterfaceType, *ast.MapType,
		*ast.ChanType, *ast.FuncType, *ast.ArrayType:
		// anonymous types carry no methods — the select is a field or
		// fails at run time; neither binds a cell.
		return false
	}
	// pkg.T selects, F[T] instantiations, and anything else: the member
	// could be a pointer-receiver method we cannot see.
	return true
}

// indexSelectNeedsCell resolves member sel on the indexed type: a
// method's receiver kind answers directly; a direct struct field needs
// no cell; anything else — a promoted member or a member on an
// uninspectable spec — could be a pointer method, so it boxes.
func indexSelectNeedsCell(tdi *index.TypeDeclInfo, sel string) bool {
	if d := tdi.Methods[sel]; d != nil && d.Func != nil && d.Func.Recv != nil && len(d.Func.Recv.List) > 0 {
		_, ptr := d.Func.Recv.List[0].Type.(*ast.StarExpr)
		return ptr
	}
	if tdi.Decl == nil {
		return false
	}
	ts, ok := tdi.Decl.Spec.(*ast.TypeSpec)
	if !ok {
		return false
	}
	switch st := ts.Type.(type) {
	case *ast.InterfaceType:
		return false // dynamic dispatch
	case *ast.StructType:
		if st.Fields != nil {
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					if embeddedName(f.Type) == sel {
						return false
					}
					continue
				}
				for _, n := range f.Names {
					if n.Name == sel {
						return false
					}
				}
			}
		}
		// not a direct member — a promoted member whose receiver kind
		// lives in the embedded type's decl.
		return true
	}
	// a named non-struct: the only members are methods, already checked.
	return false
}
