package compile

import (
	"go/ast"

	"github.com/podhmo/minigo/syntax"
)

// typSpecFile returns the file a named type's spec was declared in —
// the scope its field and element names resolve under. A local `type`
// decl has no file of its own (its scope is the use site's) and an
// unnamed or unresolvable type reports none.
func (c *compiler) typSpecFile(te ast.Expr) *syntax.File {
	t := ast.Unparen(te)
	if _, ok := t.(*ast.Ident); ok {
		id := t.(*ast.Ident)
		if c.fs.lookupBinding(id.Name) != nil {
			return nil // a local decl's names live in the use site's scope
		}
		if ts := pkgTypeSpec(c.pkg, id.Name); ts != nil {
			return specFileOf(c.pkg, ts.Pos())
		}
		return nil
	}
	if _, ok := t.(*ast.SelectorExpr); ok {
		if ref, name, ok := c.selTypRef(t); ok {
			if p, ts := c.selTypeSpec(ref, name); ts != nil {
				return specFileOf(p, ts.Pos())
			}
		}
	}
	return nil
}

// structFieldTypExpr is fieldTypExpr that also sees through a pointer
// to the struct and accepts an anonymous struct type (direct fields
// only for the latter).
func (c *compiler) structFieldTypExpr(base ast.Expr, name string) ast.Expr {
	if base == nil {
		return nil
	}
	if st, isPtr := ast.Unparen(base).(*ast.StarExpr); isPtr {
		base = st.X
	}
	if te, _ := c.fieldTypExpr(base, name, 4); te != nil {
		return te
	}
	st, ok := c.unfoldTypExpr(base, 8).(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				return f.Type
			}
		}
	}
	return nil
}

// fieldTypExpr reports a named field's declared type expression and
// the file that declaring spec lives in — the scope the field type's
// names resolve under. A promoted field carries its own spec's file,
// not the outer type's.
func (c *compiler) fieldTypExpr(te ast.Expr, sel string, depth int) (ast.Expr, *syntax.File) {
	if depth <= 0 {
		return nil, nil
	}
	ts := c.typeSpecOf(te)
	if ts == nil {
		return nil, nil
	}
	st, ok := ast.Unparen(ts.Type).(*ast.StructType)
	if !ok {
		return nil, nil
	}
	// direct fields win over promoted ones regardless of declaration
	// order — Go's shallower-depth rule. A first pass over named
	// fields, then the embeds; the other order lets a promoted field
	// shadow a direct one, which gc rejects in the opposite direction.
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == sel {
				return f.Type, c.typSpecFile(te)
			}
		}
	}
	var promoted ast.Expr
	var promotedFile *syntax.File
	for _, f := range st.Fields.List {
		if len(f.Names) != 0 {
			continue
		}
		// an embedded field promotes its own fields; `*T` embeds
		// promote like `T` (a parens/star wrapper is not a selector).
		emb := ast.Unparen(f.Type)
		if s, ok := emb.(*ast.StarExpr); ok {
			emb = ast.Unparen(s.X)
		}
		t, tf := c.fieldTypExpr(emb, sel, depth-1)
		if t == nil {
			continue
		}
		if promoted != nil {
			// two same-depth promotions — gc rejects the selector
			// itself as ambiguous, so the operand is opaque.
			return nil, nil
		}
		promoted, promotedFile = t, tf
	}
	return promoted, promotedFile
}
