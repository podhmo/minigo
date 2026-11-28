// Package-scope array length helpers for the constant evaluator. len/cap
// of an array operand is constant from the operand's type alone, so these
// reads must never evaluate or initialize the operand's package.
package compile

import (
	"go/ast"
	"go/constant"
	"go/token"

	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

func (c *compiler) typeScope(pkg *runtime.Package, file *syntax.File) *compiler {
	if pkg == c.pkg && file == c.file {
		return c
	}
	nc := &compiler{pkg: pkg, file: file, fs: newFScope(nil), iotaVal: -1, strictLazy: c.strictLazy}
	nc.fs.pushBlock()
	return nc
}

func singleResultType(ft *ast.FuncType) ast.Expr {
	if ft == nil || ft.Results == nil || len(ft.Results.List) != 1 {
		return nil
	}
	f := ft.Results.List[0]
	if len(f.Names) > 1 {
		return nil
	}
	return f.Type
}

// inferredValueType reads the static type of simple package-variable
// initializers. Function declarations are inspected through the source
// index only; the function is never called.
func (c *compiler) inferredValueType(nc *compiler, pkg *runtime.Package, v ast.Expr) ast.Expr {
	switch x := ast.Unparen(v).(type) {
	case *ast.CompositeLit:
		return x.Type
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			if t := c.inferredValueType(nc, pkg, x.X); t != nil {
				return &ast.StarExpr{X: t}
			}
		}
	case *ast.CallExpr:
		switch f := ast.Unparen(x.Fun).(type) {
		case *ast.FuncLit:
			return singleResultType(f.Type)
		case *ast.Ident:
			if pkg != nil && pkg.Index != nil {
				if fd := pkg.Index.Funcs[f.Name]; fd != nil && fd.Func != nil {
					return singleResultType(fd.Func.Type)
				}
			}
			// Otherwise the call may be a conversion through a named type.
			return f
		case *ast.SelectorExpr:
			if id, ok := ast.Unparen(f.X).(*ast.Ident); ok {
				if ref, isPkg := nc.importRef(id.Name); isPkg {
					res := nc.askIndexed(ref)
					if (res.Kind == semantics.Found || res.Kind == semantics.Deferred) && res.Val != nil && res.Val.Index != nil {
						if fd := res.Val.Index.Funcs[f.Sel.Name]; fd != nil && fd.Func != nil {
							return singleResultType(fd.Func.Type)
						}
					}
				}
			}
			// Otherwise the call may be a conversion through an imported named type.
			return f
		case *ast.ArrayType:
			return f
		}
	}
	return nil
}

func (c *compiler) packageVarType(pkg *runtime.Package, file *syntax.File, name string) (ast.Expr, bool) {
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
		te = c.inferredValueType(c.typeScope(pkg, file), pkg, vs.Values[vd.NameIdx])
	}
	if te == nil {
		return nil, false
	}
	return te, true
}

// varTypeOperand resolves the declared type of a variable operand without
// resolving the variable's value. The returned scope mode is the mode its
// type expression belongs to: local scope for a local variable, package
// scope outside a const spec for a package variable.
func (c *compiler) varTypeOperand(e ast.Expr, pkgIota int) (ast.Expr, *compiler, int, bool) {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident:
		if pkgIota == -1 {
			if b := c.fs.lookupBinding(x.Name); b != nil && b.declTyp != nil {
				return b.declTyp, c, pkgIota, true
			}
		}
		if te, ok := c.packageVarType(c.pkg, c.file, x.Name); ok {
			return te, c.typeScope(c.pkg, c.file), -2, true
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
					if te, ok := c.packageVarType(p, vd.File, x.Name); ok {
						return te, c.typeScope(p, vd.File), -2, true
					}
				}
			}
		}
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return nil, nil, 0, false
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return nil, nil, 0, false
		}
		res := c.askIndexed(ref)
		if res.Kind != semantics.Found && res.Kind != semantics.Deferred {
			return nil, nil, 0, false
		}
		p := res.Val
		if p == nil || p.Index == nil {
			return nil, nil, 0, false
		}
		vd := p.Index.Vars[x.Sel.Name]
		if vd == nil {
			return nil, nil, 0, false
		}
		if te, ok := c.packageVarType(p, vd.File, x.Sel.Name); ok {
			return te, c.typeScope(p, vd.File), -2, true
		}
	}
	return nil, nil, 0, false
}

func (c *compiler) arrayTypeLen(nc *compiler, te ast.Expr, depth, mode int) (constant.Value, bool) {
	at, ok := ast.Unparen(nc.unfoldTypExpr(te, 8)).(*ast.ArrayType)
	if !ok || at.Len == nil {
		return nil, false
	}
	lv, _, ok := nc.scopeConstVal(at.Len, depth+1, mode)
	if !ok || lv.Kind() != constant.Int {
		return nil, false
	}
	return lv, true
}

func (c *compiler) arrayLenOperand(e ast.Expr, depth, pkgIota int) (constant.Value, bool) {
	switch x := ast.Unparen(e).(type) {
	case *ast.Ident, *ast.SelectorExpr:
		te, nc, mode, ok := c.varTypeOperand(e, pkgIota)
		if !ok {
			return nil, false
		}
		return c.arrayTypeLen(nc, te, depth, mode)
	case *ast.StarExpr:
		te, nc, mode, ok := c.varTypeOperand(x.X, pkgIota)
		if !ok {
			return nil, false
		}
		pt, ok := ast.Unparen(nc.unfoldTypExpr(te, 8)).(*ast.StarExpr)
		if !ok {
			return nil, false
		}
		return c.arrayTypeLen(nc, pt.X, depth, mode)
	}
	return nil, false
}
