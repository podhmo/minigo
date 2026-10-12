package model

import (
	"go/ast"

	xinspect "github.com/podhmo/minigo/inspect"
)

// IsInstantiation reports whether te spells a generic instantiation —
// List[int], Pair[K, V]. Unwrap cannot peel it: SymbolID does not
// resolve the instantiation itself, so the parametric spec behind it
// stays invisible to the ordinary peel.
func IsInstantiation(te *xinspect.TypeExpr) bool {
	if te == nil {
		return false
	}
	switch te.Expr().(type) {
	case *ast.IndexExpr, *ast.IndexListExpr:
		return true
	}
	return false
}

// instArgs returns the generic base and the type-argument exprs of an
// instantiation spelling, each in te's own context.
func instArgs(te *xinspect.TypeExpr) (base *xinspect.TypeExpr, args []*xinspect.TypeExpr) {
	switch e := te.Expr().(type) {
	case *ast.IndexExpr:
		return te.Sub(e.X), []*xinspect.TypeExpr{te.Sub(e.Index)}
	case *ast.IndexListExpr:
		args = make([]*xinspect.TypeExpr, 0, len(e.Indices))
		for _, ix := range e.Indices {
			args = append(args, te.Sub(ix))
		}
		return te.Sub(e.X), args
	}
	return nil, nil
}

// Instantiate substitutes a generic instantiation's type arguments
// into the declaration's spec: List[int] resolves `type List[T any]
// []T` and splices int into T's positions, yielding []int. The result
// is viewed in the spec's file context (the context Unwrap would hand
// to a non-generic spec), so it is only built when every name the
// arguments carry resolves identically in both contexts — otherwise a
// spliced name could silently misresolve (e.g. a package-qualified
// argument whose qualifier the spec's file does not import).
//
// The second result is false when te is not an instantiation, the base
// does not resolve to a generic type decl, the argument count does not
// match, or an argument fails the context check. The inspect layer
// deliberately does not instantiate — this is a consumer-side rewrite
// over the spec's AST.
func Instantiate(res xinspect.Resolver, te *xinspect.TypeExpr) (*xinspect.TypeExpr, bool) {
	base, args := instArgs(te)
	if base == nil {
		return nil, false
	}
	sid, ok := base.SymbolID()
	if !ok || xinspect.NoDeclPath(sid.PackagePath) {
		return nil, false
	}
	d, err := res(sid)
	if err != nil || d == nil {
		return nil, false
	}
	params, err := xinspect.TypeParamsOf(d)
	if err != nil {
		return nil, false
	}
	var names []string
	for _, p := range params {
		names = append(names, p.Names...)
	}
	if len(names) == 0 || len(names) != len(args) {
		return nil, false
	}
	spec, err := xinspect.DefOf(d)
	if err != nil {
		return nil, false
	}
	mapping := make(map[string]ast.Expr, len(args))
	for i, name := range names {
		if !argResolvableIn(spec, args[i]) {
			return nil, false
		}
		mapping[name] = args[i].Expr()
	}
	return spec.Sub(substExpr(spec.Expr(), mapping)), true
}

// argResolvableIn reports whether every identifier and selector inside
// arg's expr resolves to the same symbol in arg's own context and in
// spec's context. A selector is checked as a unit — the package
// qualifier means nothing alone — so its children are not descended.
// An identifier the spec context would resolve differently (a type
// parameter of the generic decl, or a same-named sibling type of the
// declaring package) fails the check: splicing it would rebind it.
func argResolvableIn(spec, arg *xinspect.TypeExpr) bool {
	ok := true
	ast.Inspect(arg.Expr(), func(n ast.Node) bool {
		if !ok || n == nil {
			return false
		}
		var e ast.Expr
		switch x := n.(type) {
		case *ast.Ident:
			e = x
		case *ast.SelectorExpr:
			e = x
		default:
			return true
		}
		sa, oka := arg.Sub(e).SymbolID()
		sb, okb := spec.Sub(e).SymbolID()
		if oka != okb || (oka && sa != sb) {
			ok = false
			return false
		}
		_, isSel := e.(*ast.SelectorExpr)
		return !isSel
	})
	return ok
}

// substExpr rebuilds a generic spec's expr with each type-parameter
// identifier replaced by the argument's expr. Argument nodes are
// spliced in, not copied — positions ride on the TypeExpr's context,
// not the node. Field names are not substituted, only type positions.
func substExpr(e ast.Expr, args map[string]ast.Expr) ast.Expr {
	switch t := e.(type) {
	case *ast.Ident:
		if r, ok := args[t.Name]; ok {
			return r
		}
		return t
	case *ast.ArrayType:
		return &ast.ArrayType{Lbrack: t.Lbrack, Len: substOrNil(t.Len, args), Elt: substExpr(t.Elt, args)}
	case *ast.StarExpr:
		return &ast.StarExpr{Star: t.Star, X: substExpr(t.X, args)}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Ellipsis: t.Ellipsis, Elt: substExpr(t.Elt, args)}
	case *ast.ParenExpr:
		return &ast.ParenExpr{Lparen: t.Lparen, X: substExpr(t.X, args), Rparen: t.Rparen}
	case *ast.MapType:
		return &ast.MapType{Map: t.Map, Key: substExpr(t.Key, args), Value: substExpr(t.Value, args)}
	case *ast.ChanType:
		return &ast.ChanType{Begin: t.Begin, Arrow: t.Arrow, Dir: t.Dir, Value: substExpr(t.Value, args)}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: substExpr(t.X, args), Lbrack: t.Lbrack, Index: substExpr(t.Index, args), Rbrack: t.Rbrack}
	case *ast.IndexListExpr:
		ix := make([]ast.Expr, len(t.Indices))
		for i, e := range t.Indices {
			ix[i] = substExpr(e, args)
		}
		return &ast.IndexListExpr{X: substExpr(t.X, args), Lbrack: t.Lbrack, Indices: ix, Rbrack: t.Rbrack}
	case *ast.FuncType:
		return &ast.FuncType{Func: t.Func, TypeParams: t.TypeParams, Params: substFieldList(t.Params, args), Results: substFieldList(t.Results, args)}
	case *ast.StructType:
		return &ast.StructType{Struct: t.Struct, Fields: substFieldList(t.Fields, args), Incomplete: t.Incomplete}
	case *ast.InterfaceType:
		return &ast.InterfaceType{Interface: t.Interface, Methods: substFieldList(t.Methods, args), Incomplete: t.Incomplete}
	case *ast.UnaryExpr:
		return &ast.UnaryExpr{OpPos: t.OpPos, Op: t.Op, X: substExpr(t.X, args)}
	case *ast.BinaryExpr:
		return &ast.BinaryExpr{X: substExpr(t.X, args), OpPos: t.OpPos, Op: t.Op, Y: substExpr(t.Y, args)}
	default:
		return t
	}
}

// substOrNil substitutes into an optional expr position (array length).
func substOrNil(e ast.Expr, args map[string]ast.Expr) ast.Expr {
	if e == nil {
		return nil
	}
	return substExpr(e, args)
}

// substFieldList rebuilds a field list, substituting only the type of
// each field — names, tags and docs keep their nodes.
func substFieldList(fl *ast.FieldList, args map[string]ast.Expr) *ast.FieldList {
	if fl == nil {
		return nil
	}
	list := make([]*ast.Field, len(fl.List))
	for i, f := range fl.List {
		nf := *f
		nf.Type = substExpr(f.Type, args)
		list[i] = &nf
	}
	return &ast.FieldList{Opening: fl.Opening, List: list, Closing: fl.Closing}
}
