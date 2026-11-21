package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// importPackageAt resolves the package an unqualified qualifier names:
// a file-scope import (alias, or the package clause's real name for an
// unaliased import whose clause differs from the path basename), the
// context's own package when spelled by path, or — as exprOf emits —
// an import path loaded directly.
func (e *Engine) importPackageAt(from *runtime.TypeDef, name string) (*runtime.Package, error) {
	if from == nil || from.Pkg == nil {
		return nil, fmt.Errorf("cannot resolve package %s", name)
	}
	var scope map[string]*runtime.ImportRef
	if from.File != nil {
		scope = from.Pkg.Scopes[from.File]
	}
	ref, ok := scope[name]
	if !ok && from.File != nil {
		for _, r := range from.Pkg.Imports[from.File] {
			if r.Alias != "" {
				continue
			}
			if p, err := r.Materialize(); err == nil && p != nil && p.Name == name {
				ref, ok = r, true
				break
			}
		}
	}
	if !ok {
		if from.Pkg.Path == name {
			return from.Pkg, nil
		}
		ref = &runtime.ImportRef{
			Path: name,
			Load: func(path string) (*runtime.Package, error) {
				return e.loadPath(context.Background(), path)
			},
		}
	}
	return ref.Materialize()
}

// constExprOf implements the Hooks.DeferredConst hook (M4-7c): whether
// the site's expression is a constant expression under ctx — compile's
// isConstExprDepth answered under AllowIndex: qualified package members
// answer from the package's index and globals at runtime.
func (e *Engine) constExprOf(site *bytecode.CallSite, ctx *runtime.TypeDef) (bool, error) {
	return e.constExpr(ctx, site.Consts, site.Expr)
}

// constExpr is the runtime counterpart of compile's isConstExprDepth —
// gc's rule, not the compile-time conservative one: where compile had
// to be sure, the runtime can look a member up for real.
func (e *Engine) constExpr(ctx *runtime.TypeDef, consts map[string]bool, x ast.Expr) (bool, error) {
	switch t := x.(type) {
	case *ast.ParenExpr:
		return e.constExpr(ctx, consts, t.X)
	case *ast.BasicLit:
		return true, nil
	case *ast.Ident:
		switch t.Name {
		case "true", "false", "iota":
			return true, nil
		}
		if v, ok := consts[t.Name]; ok {
			// a local declaration answers for the name — const or not.
			return v, nil
		}
		if ctx == nil || ctx.Pkg == nil {
			return false, nil
		}
		if ctx.Pkg.Index != nil {
			if _, ok := ctx.Pkg.Index.Consts[t.Name]; ok {
				return true, nil
			}
		}
		if v, ok := ctx.Pkg.Globals.Get(t.Name); ok {
			_, isU := v.(*runtime.UConst)
			return isU, nil
		}
		return false, nil
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return false, nil
		}
		p, err := e.importPackageAt(ctx, id.Name)
		if err != nil {
			return false, err
		}
		if p == nil {
			return false, nil
		}
		if p.Index != nil {
			if _, ok := p.Index.Consts[t.Sel.Name]; ok {
				return true, nil
			}
		}
		if v, ok := p.Globals.Get(t.Sel.Name); ok {
			_, isU := v.(*runtime.UConst)
			return isU, nil
		}
		return false, nil
	case *ast.UnaryExpr:
		switch t.Op {
		case token.ADD, token.SUB, token.XOR, token.NOT:
			return e.constExpr(ctx, consts, t.X)
		}
		return false, nil
	case *ast.BinaryExpr:
		a, err := e.constExpr(ctx, consts, t.X)
		if !a || err != nil {
			return a, err
		}
		return e.constExpr(ctx, consts, t.Y)
	case *ast.CallExpr:
		if t.Ellipsis.IsValid() || len(t.Args) == 0 {
			return false, nil
		}
		switch f := ast.Unparen(t.Fun).(type) {
		case *ast.Ident:
			// a same-package decl shadows the builtin name.
			if ctx != nil && ctx.Pkg != nil && ctx.Pkg.Index != nil {
				if _, shadow := ctx.Pkg.Index.Lookup(f.Name); shadow {
					return false, nil
				}
			}
			switch f.Name {
			case "len", "cap":
				if len(t.Args) != 1 {
					return false, nil
				}
				arg := t.Args[0]
				if f.Name == "len" {
					if c, err := e.constExpr(ctx, consts, arg); c || err != nil {
						return c, err // len of a constant string
					}
				}
				// len/cap of an array-typed operand is constant even
				// though the operand is not — `len(p.A)` asks the
				// declared type, so the runtime answer resolves it
				// instead of probing the operand's constness.
				if e.lenOperandCalls(ctx, arg) {
					return false, nil
				}
				td, _, err := e.valueTyp(ctx, arg)
				if err != nil || td == nil {
					return false, nil
				}
				u, err := e.underlying(td)
				if err != nil || u == nil {
					return false, nil
				}
				if u.Kind == runtime.KindPointer && u.Elem != nil {
					if u2, err2 := e.underlying(u.Elem); err2 == nil && u2 != nil {
						u = u2
					}
				}
				return runtime.ArrayTypedef(u), nil
			case "min", "max", "real", "imag", "complex":
				for _, a := range t.Args {
					c, err := e.constExpr(ctx, consts, a)
					if !c || err != nil {
						return c, err
					}
				}
				return true, nil
			}
			// a conversion keeps the constant when the target's
			// underlying is a basic type (`[]byte("s")` does not).
			td, err := e.resolveTypeRef(ctx, f)
			if err != nil || td == nil {
				return false, nil
			}
			u, err := e.underlying(td)
			if err != nil || u == nil || u.Kind != runtime.KindNamedBasic {
				return false, nil
			}
			if len(t.Args) != 1 {
				return false, nil
			}
			return e.constExpr(ctx, consts, t.Args[0])
		case *ast.SelectorExpr:
			switch f.Sel.Name {
			case "Sizeof", "Alignof", "Offsetof":
			default:
				return false, nil
			}
			id, ok := f.X.(*ast.Ident)
			if !ok || len(t.Args) != 1 {
				return false, nil
			}
			p, err := e.importPackageAt(ctx, id.Name)
			if err != nil {
				return false, err
			}
			return p != nil && p.Path == "unsafe", nil
		}
	}
	return false, nil
}

// lenOperandCalls mirrors the compile-side len/cap rule: an operand
// containing a call or receive is never a constant operand, while a
// conversion is transparent and descends into its args.
func (e *Engine) lenOperandCalls(ctx *runtime.TypeDef, ex ast.Expr) bool {
	calls := false
	ast.Inspect(ex, func(n ast.Node) bool {
		if calls || n == nil {
			return false
		}
		switch t := n.(type) {
		case *ast.FuncLit:
			return false // body calls run at invocation, not operand time
		case *ast.CallExpr:
			if td, err := e.resolveTypeRef(ctx, t.Fun); err == nil && td != nil {
				return true
			}
			calls = true
			return false
		case *ast.UnaryExpr:
			if t.Op == token.ARROW {
				calls = true
				return false
			}
		}
		return true
	})
	return calls
}

// valueTypOf implements the Hooks.DeferredValueTyp hook (M4-7c): the
// typedef a value expression carries — a qualified package member's
// declared or inferred type, or an untyped constant's default type.
// nil is a lawful answer: the gate stays silent on what it can't type.
func (e *Engine) valueTypOf(site *bytecode.CallSite, ctx *runtime.TypeDef) (*runtime.TypeDef, bool, error) {
	return e.valueTyp(ctx, site.Expr)
}

func (e *Engine) valueTyp(ctx *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, bool, error) {
	return e.valueTypDepth(ctx, x, 0)
}

func (e *Engine) valueTypDepth(ctx *runtime.TypeDef, x ast.Expr, depth int) (*runtime.TypeDef, bool, error) {
	switch t := x.(type) {
	case *ast.ParenExpr:
		return e.valueTypDepth(ctx, t.X, depth)
	case *ast.BasicLit:
		return basicLitTypedef(t.Kind), true, nil
	case *ast.Ident:
		if t.Name == "nil" {
			// nil is never a package member — looking it up would
			// re-enter the package's init-once during a deferred
			// gate that runs inside that very initializer.
			return nil, false, nil
		}
		if ctx == nil || ctx.Pkg == nil {
			return nil, false, nil
		}
		return e.memberTypOf(ctx.Pkg, t.Name, depth+1)
	case *ast.UnaryExpr:
		if t.Op != token.SUB && t.Op != token.ADD {
			return nil, false, nil
		}
		return e.valueTypDepth(ctx, t.X, depth+1)
	case *ast.CallExpr:
		// a conversion T(v) carries T's typedef; anything else is a
		// call whose result type this resolver cannot infer.
		td, err := e.resolveTypeRef(ctx, t.Fun)
		if err != nil {
			return nil, false, nil
		}
		return td, false, nil
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return nil, false, nil
		}
		p, err := e.importPackageAt(ctx, id.Name)
		if err != nil {
			return nil, false, err
		}
		if p == nil {
			return nil, false, nil
		}
		return e.memberTypOf(p, t.Sel.Name, depth+1)
	}
	return nil, false, nil
}

// basicLitTypedef is the default typedef of an untyped literal — the
// runtime answer to a const decl's `const C = 'a'`.
func basicLitTypedef(k token.Token) *runtime.TypeDef {
	switch k {
	case token.CHAR:
		return runtime.BasicTypedef("rune")
	case token.STRING:
		return runtime.BasicTypedef("string")
	case token.INT:
		return runtime.BasicTypedef("int")
	case token.FLOAT:
		return runtime.BasicTypedef("float64")
	case token.IMAG:
		return runtime.BasicTypedef("complex128")
	}
	return nil
}

// memberTyp is the typedef a package member carries for the compare
// gate: a Named value's tag, a cell's declared slot, an untyped
// constant's default type. Non-value members and unshaped values give
// nil — the gate stays silent.
// memberTyp is the typedef a package member carries for the compare
// gate — plus whether the member is an untyped constant, which a
// maybe-untyped operand adopts instead of gating on its default type.
func memberTyp(m runtime.Value) (*runtime.TypeDef, bool) {
	for {
		switch v := m.(type) {
		case *runtime.Named:
			return v.Typ, false
		case *runtime.Cell:
			if v.Typ != nil {
				return v.Typ, false
			}
			m = v.Elem
		case *runtime.UConst:
			return uconstTypedef(v), true
		default:
			return nil, false
		}
	}
}

// uconstTypedef is the default typedef of an untyped constant — the
// type it takes without a coercing context.
func uconstTypedef(u *runtime.UConst) *runtime.TypeDef {
	if u == nil || u.V == nil {
		return nil
	}
	switch u.V.Kind() {
	case constant.Bool:
		return runtime.BasicTypedef("bool")
	case constant.String:
		return runtime.BasicTypedef("string")
	case constant.Int:
		if u.Rune {
			return runtime.BasicTypedef("rune")
		}
		return runtime.BasicTypedef("int")
	case constant.Float:
		return runtime.BasicTypedef("float64")
	case constant.Complex:
		return runtime.BasicTypedef("complex128")
	}
	return nil
}
