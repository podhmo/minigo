package compile

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
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
	need, why := c.selectNeedsCell(base, sel)
	if !need {
		return
	}
	if id := rootIdent(base); id != nil {
		c.fs.boxName(id.Name, "select:"+why)
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

// cellVerdict is the tri-state answer to "can selecting sel on this
// operand bind its storage cell": cellYes binds it (a pointer-receiver
// method), cellNo provably cannot (a field, a value-receiver method,
// interface dispatch, a pointer operand, no member at all), and
// cellUnknown is unprovable at compile time. Boxing treats cellUnknown
// like cellNo — P4-2b's promote-on-demand covers a late ref — while a
// select's base operand keeps the storage ref like cellYes, since a
// detached value would silently drop a pointer receiver's writes.
type cellVerdict int

const (
	cellUnknown cellVerdict = iota
	cellNo
	cellYes
)

// selectNeedsCell reports whether selecting sel on the ref operand of
// base can keep the operand's cell alive past the select, plus the
// attribution reason for spike measurement. Only a proven escape boxes
// upfront: P4-2b's promote-on-demand keeps every ref path correct
// without the pre-alloc, so an unresolvable operand — a receiver whose
// declared type never reached the binding, a promoted member on an
// uninspectable spec — stays plain and promotes lazily if the select
// turns out to need the cell at run time.
func (c *compiler) selectNeedsCell(base ast.Expr, sel string) (bool, string) {
	te, kind := c.baseStaticTyp(base)
	if kind != baseTyp {
		return false, ""
	}
	v, why := c.typSelectNeedsCellDepth(te, sel, 4)
	return v == cellYes, why
}

// selectorBaseNoCell reports whether the operand of `x.sel` reads as a
// plain value with no storage ref: the static type must resolve and
// the member must provably not bind the operand's cell. Interface-typed
// and detached-copy operands also read as values (dispatch binds the
// payload, a copy owns no cell). Everything unproven keeps the ref.
func (c *compiler) selectorBaseNoCell(x ast.Expr, sel string) bool {
	te, kind := c.baseStaticTyp(x)
	switch kind {
	case baseIface, baseCopy:
		return true
	case baseTyp:
		v, _ := c.typSelectNeedsCellDepth(te, sel, 4)
		return v == cellNo
	}
	return false
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

// typSelectNeedsCellDepth classifies a select on an operand whose
// static type expression is te: a pointer-receiver method binds the
// cell (cellYes); fields, value-receiver methods, interfaces and
// memberless types provably do not (cellNo); everything unresolvable is
// cellUnknown — boxing can afford to treat it as a lazy promote, but a
// base operand must keep its storage ref. depth bounds alias-chain
// resolution (`type A = B = C`).
func (c *compiler) typSelectNeedsCellDepth(te ast.Expr, sel string, depth int) (cellVerdict, string) {
	t := ast.Unparen(te)
	switch t := t.(type) {
	case *ast.StarExpr:
		// the operand is already a pointer: the select binds the
		// pointer value, never the cell holding it.
		return cellNo, ""
	case *ast.Ident:
		if t.NamePos == token.NoPos && predeclaredTypeNames[t.Name] {
			// a positionless name is declTypCanon's marker for a name
			// that already resolved to a builtin at bind time — a
			// user-declared `type int` can never claim it.
			return cellNo, ""
		}
		if b := c.fs.lookupBinding(t.Name); b != nil && b.typeDecl {
			// a local `type` decl cannot carry methods: an interface
			// dispatches dynamically, a struct member is a field or a
			// promoted member of an embedded field. The declTyp spelling
			// may predate this shadow and name the package-level type
			// instead (`func f(s S)` inside a `type S int` block) —
			// union the answers so either referent's pointer method
			// still boxes, at depth-1 so a shadowed embed cycle stays
			// bounded; a non-yes verdict stays unproven, since the
			// referents can disagree.
			if c.pkg != nil && c.pkg.Index != nil {
				if tdi := c.pkg.Index.Types[t.Name]; tdi != nil {
					if v, why := c.indexSelectNeedsCell(tdi, sel, depth-1); v == cellYes {
						return v, why
					}
				}
			}
			return cellUnknown, ""
		}
		if _, bound := c.binds[t.Name]; bound {
			return cellUnknown, "" // a type parameter's receiver is unprovable
		}
		if tdi := c.typeDeclOf(t); tdi != nil {
			return c.indexSelectNeedsCell(tdi, sel, depth)
		}
		if predeclaredTypeNames[t.Name] {
			return cellNo, ""
		}
		return cellUnknown, "" // an unresolved type name stays unproven
	case *ast.SelectorExpr:
		// `pkg.T` — an indexed source package's declared type answers
		// the member's receiver kind like a local decl.
		if id, ok := ast.Unparen(t.X).(*ast.Ident); ok && c.fs.lookupBinding(id.Name) == nil {
			if ref, isPkg := c.importRef(id.Name); isPkg {
				if res := c.askIndexed(ref); res.Kind == semantics.Found {
					if p := res.Val; p.Index != nil {
						if tdi := p.Index.Types[t.Sel.Name]; tdi != nil {
							return c.indexSelectNeedsCell(tdi, sel, depth)
						}
					}
				}
			}
		}
		return cellUnknown, "" // a host or unindexed member is unproven
	case *ast.StructType:
		// an anonymous struct declares no methods, but `struct{ N }`
		// promotes N's — a pointer receiver still binds the cell.
		return c.structFieldNeedsCell(t, sel, depth)
	case *ast.InterfaceType, *ast.MapType,
		*ast.ChanType, *ast.FuncType, *ast.ArrayType:
		// other anonymous types carry no methods — the select is a
		// field or fails at run time; neither binds a cell.
		return cellNo, ""
	}
	// F[T] instantiations and anything else: the member could be a
	// pointer-receiver method we cannot prove.
	return cellUnknown, ""
}

// indexSelectNeedsCell resolves member sel on the indexed type: a
// method's receiver kind answers directly; a direct struct field needs
// no cell; anything else — a promoted member or a member on an
// uninspectable spec — could be a pointer method, so it boxes. An alias
// spec (`type A = T`) declares no members of its own: the member set is
// the target type's, resolved through the same ident rules (depth caps
// alias chains).
func (c *compiler) indexSelectNeedsCell(tdi *index.TypeDeclInfo, sel string, depth int) (cellVerdict, string) {
	if d := tdi.Methods[sel]; d != nil && d.Func != nil && d.Func.Recv != nil && len(d.Func.Recv.List) > 0 {
		_, ptr := ast.Unparen(d.Func.Recv.List[0].Type).(*ast.StarExpr)
		if ptr {
			return cellYes, "ptrrecv"
		}
		return cellNo, ""
	}
	if tdi.Decl == nil {
		return cellUnknown, ""
	}
	ts, ok := tdi.Decl.Spec.(*ast.TypeSpec)
	if !ok {
		return cellUnknown, ""
	}
	if ts.Assign.IsValid() {
		// `type A = T` — members live on T's declaration. A chain past
		// the depth cap is unprovable.
		if depth <= 0 {
			return cellUnknown, ""
		}
		return c.typSelectNeedsCellDepth(ts.Type, sel, depth-1)
	}
	switch st := ts.Type.(type) {
	case *ast.InterfaceType:
		return cellNo, "" // dynamic dispatch
	case *ast.StructType:
		return c.structFieldNeedsCell(st, sel, depth)
	}
	// a named non-struct: the only members are methods, already checked.
	return cellNo, ""
}

// structFieldNeedsCell classifies sel against a struct's fields: a
// direct or embedded-name field is cellNo; a promoted member's receiver
// kind lives in an embedded type's decl (an embedded `*U` promotes U's
// methods including pointer receivers — the cell is the ref target
// either way); an uninspectable embed is cellUnknown. Shared by the
// named-decl and anonymous-struct paths — an anonymous `struct{ N }`
// promotes N's methods the same way.
func (c *compiler) structFieldNeedsCell(st *ast.StructType, sel string, depth int) (cellVerdict, string) {
	if st.Fields == nil {
		return cellNo, ""
	}
	var embedded []ast.Expr
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			if embeddedName(f.Type) == sel {
				return cellNo, ""
			}
			embedded = append(embedded, f.Type)
			continue
		}
		for _, n := range f.Names {
			if n.Name == sel {
				return cellNo, ""
			}
		}
	}
	if len(embedded) == 0 {
		// a direct field, a value-receiver method, or no member at all
		// — none binds the cell.
		return cellNo, ""
	}
	if depth <= 0 {
		return cellUnknown, ""
	}
	unknown := false
	for _, et := range embedded {
		if se, ok := ast.Unparen(et).(*ast.StarExpr); ok {
			et = se.X
		}
		v, _ := c.typSelectNeedsCellDepth(et, sel, depth-1)
		if v == cellYes {
			return cellYes, "ptrrecv"
		}
		if v == cellUnknown {
			unknown = true
		}
	}
	if unknown {
		// a promoted member on an uninspectable embed may still be a
		// pointer method.
		return cellUnknown, ""
	}
	return cellNo, ""
}
