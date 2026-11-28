// Package-scope array length helpers for the constant evaluator. len/cap
// of an array operand is constant from the operand's type alone, so these
// reads must never evaluate or initialize the operand's package.
package compile

import (
	"go/ast"
	"go/constant"

	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

func (c *compiler) arrayVarLen(pkg *runtime.Package, file *syntax.File, name string, depth int) (constant.Value, bool) {
	if pkg == nil || pkg.Index == nil {
		return nil, false
	}
	vd := pkg.Index.Vars[name]
	if vd == nil {
		return nil, false
	}
	vs, ok := vd.Spec.(*ast.ValueSpec)
	if !ok {
		return nil, false
	}
	te := vs.Type
	if te == nil && len(vs.Values) == len(vs.Names) && vd.NameIdx < len(vs.Values) {
		if cl, ok := ast.Unparen(vs.Values[vd.NameIdx]).(*ast.CompositeLit); ok {
			te = cl.Type
		}
	}
	if te == nil {
		return nil, false
	}
	nc := c
	if pkg != c.pkg || file != c.file {
		nc = &compiler{pkg: pkg, file: file, fs: newFScope(nil), iotaVal: -1, strictLazy: c.strictLazy}
		nc.fs.pushBlock()
	}
	at, ok := ast.Unparen(nc.unfoldTypExpr(te, 8)).(*ast.ArrayType)
	if !ok || at.Len == nil {
		return nil, false
	}
	lv, _, ok := nc.scopeConstVal(at.Len, depth+1, -2)
	if !ok || lv.Kind() != constant.Int {
		return nil, false
	}
	return lv, true
}

func (c *compiler) arrayLenOperand(e ast.Expr, depth, pkgIota int) (constant.Value, bool) {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		if pkgIota == -1 {
			if b := c.fs.lookupBinding(x.Name); b != nil && b.declTyp != nil {
				if at, ok := ast.Unparen(c.unfoldTypExpr(b.declTyp, 8)).(*ast.ArrayType); ok && at.Len != nil {
					lv, _, ok := c.scopeConstVal(at.Len, depth+1, pkgIota)
					return lv, ok && lv.Kind() == constant.Int
				}
			}
		}
		if lv, ok := c.arrayVarLen(c.pkg, c.file, x.Name, depth); ok {
			return lv, true
		}
		if c.pkg != nil && c.file != nil {
			for _, ref := range c.pkg.Imports[c.file] {
				if ref.Alias != "." {
					continue
				}
				res := c.askIndexed(ref)
				if res.Kind != semantics.Found && res.Kind != semantics.Deferred {
					continue
				}
				p := res.Val
				if p == nil || p.Index == nil {
					continue
				}
				if vd := p.Index.Vars[x.Name]; vd != nil {
					return c.arrayVarLen(p, vd.File, x.Name, depth)
				}
			}
		}
		return nil, false
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return nil, false
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return nil, false
		}
		res := c.askIndexed(ref)
		if res.Kind != semantics.Found && res.Kind != semantics.Deferred {
			return nil, false
		}
		if res.Val == nil || res.Val.Index == nil {
			return nil, false
		}
		vd := res.Val.Index.Vars[x.Sel.Name]
		if vd == nil {
			return nil, false
		}
		return c.arrayVarLen(res.Val, vd.File, x.Sel.Name, depth)
	}
	return nil, false
}
