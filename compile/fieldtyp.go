package compile

import (
	"go/ast"

	"github.com/podhmo/minigo/syntax"
)

// structFieldTypExpr is fieldTypExpr that also sees through a pointer
// to the struct and accepts an anonymous struct type (direct fields
// only for the latter).
func (c *compiler) structFieldTypExpr(base ast.Expr, file *syntax.File, name string) (ast.Expr, *syntax.File) {
	if base == nil {
		return nil, nil
	}
	if st, isPtr := ast.Unparen(base).(*ast.StarExpr); isPtr {
		base = st.X
	}
	if te, tf := c.fieldTypExpr(base, file, name, 4); te != nil {
		return te, tf
	}
	st, ok := c.unfoldTypExpr(base, 8).(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil, nil
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				// an anonymous struct's field types spell where the
				// struct type did.
				return f.Type, file
			}
		}
	}
	return nil, nil
}

// fieldTypExpr reports a named field's declared type expression and
// the file that declaring spec lives in — the scope the field type's
// names resolve under. file is where te itself was spelled (nil for
// the use site's scope): a name written in a package decl resolves in
// package scope, so a use-site local of the same name cannot steal
// it. A promoted field carries its own spec's file, not the outer
// type's.
func (c *compiler) fieldTypExpr(te ast.Expr, file *syntax.File, sel string, depth int) (ast.Expr, *syntax.File) {
	if depth <= 0 {
		return nil, nil
	}
	var ts *ast.TypeSpec
	if file != nil {
		// spelled in a package file — resolve in package scope
		// (locals and universe shadows belong to the use site).
		if id, ok := ast.Unparen(te).(*ast.Ident); ok {
			ts = pkgTypeSpec(c.pkg, id.Name)
		}
	} else {
		ts = c.typeSpecOf(te)
	}
	if ts == nil {
		return nil, nil
	}
	st, ok := ast.Unparen(ts.Type).(*ast.StructType)
	if !ok {
		return nil, nil
	}
	// the file deeper names were spelled in: a package spec's own
	// file; a local decl's names live in the use site's scope.
	var specFile *syntax.File
	if file != nil {
		specFile = specFileOf(c.pkg, ts.Pos())
	} else if id, ok := ast.Unparen(te).(*ast.Ident); ok && pkgTypeSpec(c.pkg, id.Name) == ts {
		specFile = specFileOf(c.pkg, ts.Pos())
	}
	// direct fields win over promoted ones regardless of declaration
	// order — Go's shallower-depth rule. A first pass over named
	// fields, then the embeds; the other order lets a promoted field
	// shadow a direct one, which gc rejects in the opposite direction.
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == sel {
				return f.Type, specFile
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
		t, tf := c.fieldTypExpr(emb, specFile, sel, depth-1)
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
