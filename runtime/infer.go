package runtime

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/syntax"
)

// Inferer learns a generic function's unbound type parameters by
// walking each parameter's declared type expression (the pattern, which
// may mention type params) alongside the argument's typedef — the
// TypeID level of inference. Values and call-site evidence stay with
// the caller; everything here is best-effort: mismatched or
// unsupported shapes simply teach nothing, and already-bound tparams
// are not re-bound (no consistency check — approximation).
type Inferer struct {
	// Underlying follows declared chains to the shape-giving typedef.
	Underlying func(td *TypeDef) *TypeDef
	// expanding marks UnifyIndices's one-shot underlying-expression
	// expansion: gc expands a pattern's named type once, so a named
	// instantiation nested inside that expansion (a recursive typedef's
	// own Name[T], or a deeper chain) gets no fallback of its own.
	expanding bool
	// ElemOf resolves a container's element typedef, including the
	// caller's own evidence (frame locals, first-element typedefs) and
	// interning; nil means "unresolvable".
	ElemOf func(td *TypeDef) *TypeDef
	// ResolveType resolves a type expr in `from`'s declaring context;
	// (nil, nil) means "unresolvable".
	ResolveType func(from *TypeDef, x ast.Expr) (*TypeDef, error)
}

func (i *Inferer) underlying(td *TypeDef) *TypeDef {
	if td == nil {
		return nil
	}
	if i.Underlying == nil {
		return td
	}
	return i.Underlying(td)
}

func (i *Inferer) elemOf(td *TypeDef) *TypeDef {
	if td == nil || i.ElemOf == nil {
		return nil
	}
	return i.ElemOf(td)
}

func (i *Inferer) resolveType(from *TypeDef, x ast.Expr) (*TypeDef, error) {
	if i.ResolveType == nil {
		return nil, nil
	}
	return i.ResolveType(from, x)
}

// UnifyDef walks the declared pattern expr against the concrete typedef.
func (i *Inferer) UnifyDef(ctx *TypeDef, tset map[string]bool, binds map[string]Value, pat ast.Expr, conc *TypeDef) {
	switch p := pat.(type) {
	case *ast.Ident:
		if tset[p.Name] && conc != nil {
			if _, ok := binds[p.Name]; !ok {
				binds[p.Name] = conc
			}
		}
		// a bound or foreign ident teaches nothing
	case *ast.ParenExpr:
		i.UnifyDef(ctx, tset, binds, p.X, conc)
	case *ast.Ellipsis:
		i.UnifyDef(ctx, tset, binds, p.Elt, conc)
	case *ast.StarExpr:
		// *T unifies only against pointer-shaped args — a slice or
		// map's element type is not a pointee and must teach nothing.
		if u := i.underlying(conc); u != nil && u.Kind == KindPointer {
			et := i.elemOf(conc)
			if et == nil {
				et = conc.Elem
			}
			if et != nil {
				i.UnifyDef(ctx, tset, binds, p.X, et)
			}
		}
	case *ast.ArrayType:
		// []T unifies only against slice/array-shaped args — a pointer's
		// pointee is not an element and must teach nothing.
		if u := i.underlying(conc); u != nil && u.Kind == KindSlice {
			et := i.elemOf(conc)
			if et == nil {
				// element named by a type the typedef cannot resolve —
				// a function-local `type` decl, say — falls back to the
				// element evidence the caller lifted off the value.
				et = conc.Elem
			}
			if et != nil {
				i.UnifyDef(ctx, tset, binds, p.Elt, et)
			}
		}
	case *ast.MapType:
		if mt, ok := conc.Anon.(*ast.MapType); ok {
			if kt, err := i.ResolveOperand(conc, mt.Key); err == nil && kt != nil {
				i.UnifyDef(ctx, tset, binds, p.Key, kt)
			}
			if vt, err := i.ResolveOperand(conc, mt.Value); err == nil && vt != nil {
				i.UnifyDef(ctx, tset, binds, p.Value, vt)
			}
		}
	case *ast.ChanType:
		if u := i.underlying(conc); u != nil && u.Kind == KindChan {
			if et := i.elemOf(conc); et != nil {
				i.UnifyDef(ctx, tset, binds, p.Value, et)
			}
		}
	case *ast.FuncType:
		cs := FuncTypeExpr(conc)
		if cs == nil {
			break
		}
		i.UnifyFields(ctx, tset, binds, p.Params, cs.Params, conc)
		i.UnifyFields(ctx, tset, binds, p.Results, cs.Results, conc)
	case *ast.IndexExpr:
		i.UnifyIndices(ctx, tset, binds, p.X, []ast.Expr{p.Index}, conc)
	case *ast.IndexListExpr:
		i.UnifyIndices(ctx, tset, binds, p.X, p.Indices, conc)
	}
}

// ResolveOperand resolves a type expression inside conc's own type
// (a map key or value) to the typedef it denotes. ResolveType peels *T
// to T — right for method sets, wrong here: map[string]E over
// map[string]*Item must bind E=*Item — so a pointer expr becomes an
// anonymous pointer typedef in conc's context.
func (i *Inferer) ResolveOperand(conc *TypeDef, x ast.Expr) (*TypeDef, error) {
	if px, ok := x.(*ast.ParenExpr); ok {
		return i.ResolveOperand(conc, px.X)
	}
	if _, ok := x.(*ast.StarExpr); ok {
		return &TypeDef{Kind: KindPointer, Anon: x, Pkg: conc.Pkg, File: conc.File, Binds: conc.Binds}, nil
	}
	return i.resolveType(conc, x)
}

// UnifyFields zips two flattened field lists (params or results): each
// declared type expr on the pattern side unifies against the resolved
// typedef on the concrete side. A pattern `...T` pairs with either
// `...U` or `[]U`.
func (i *Inferer) UnifyFields(ctx *TypeDef, tset map[string]bool, binds map[string]Value, pat, conc *ast.FieldList, concCtx *TypeDef) {
	if pat == nil || conc == nil {
		return
	}
	pe := syntax.FieldTypes(pat)
	ce := syntax.FieldTypes(conc)
	for j, p := range pe {
		if j >= len(ce) {
			break
		}
		pv := p
		cvv := ce[j]
		peEl, _ := pv.(*ast.Ellipsis)
		ceEl, _ := cvv.(*ast.Ellipsis)
		if peEl != nil && ceEl == nil {
			// variadic pattern against a non-variadic concrete func:
			// approximate by unifying the element against the param as-is.
			pv = peEl.Elt
		}
		ct, err := i.ResolveOperand(concCtx, cvv)
		if err != nil || ct == nil {
			continue
		}
		if ceEl != nil {
			// the concrete side is `...U` — the pattern sees []U
			ct = &TypeDef{Kind: KindSlice, Anon: &ast.ArrayType{Lbrack: ceEl.Pos(), Elt: ceEl.Elt}, Pkg: concCtx.Pkg, File: concCtx.File, Binds: concCtx.Binds}
		}
		i.UnifyDef(ctx, tset, binds, pv, ct)
	}
}

// UnifyIndices handles a `Name[E]`-shaped pattern against an
// instantiated concrete typedef: each tparam index binds to the
// concrete type's own binding for the base's parameter at that
// position.
func (i *Inferer) UnifyIndices(ctx *TypeDef, tset map[string]bool, binds map[string]Value, base ast.Expr, idxs []ast.Expr, conc *TypeDef) {
	bt, err := i.resolveType(ctx, base)
	if err != nil || bt == nil || len(bt.TParams) == 0 {
		return
	}
	var bound bool
	for j, x := range idxs {
		if j >= len(bt.TParams) {
			break
		}
		bv, hasBv := conc.Binds[bt.TParams[j]]
		if hasBv {
			bound = true
		}
		if id, ok := x.(*ast.Ident); ok && tset[id.Name] {
			if hasBv {
				if _, done := binds[id.Name]; !done {
					binds[id.Name] = bv
				}
			}
			continue
		}
		// a non-ident index (func() Y, []T, *T) is a pattern itself:
		// unify it against the concrete type's binding for the base's
		// parameter at this position — Mapper[X, func() Y] over
		// Mapper[int, func() string] binds Y=string.
		if td, ok := bv.(*TypeDef); hasBv && ok && td != nil {
			i.UnifyDef(ctx, tset, binds, x, td)
		}
	}
	// an unnamed concrete shape (a bare func literal's signature, a
	// composite literal's anon type) never records the instantiation's
	// binds — unify through the base type's own underlying expression
	// instead: Seq[V] over func(func(int) bool) binds V=int, like gc.
	// Skipped when the binds lookup already taught the indices (an
	// instantiated named arg), when the pattern instantiates only part
	// of the base's parameters (a leftover tparam name in the underlying
	// expr could then bind the caller's same-named parameter), or when
	// this base typedef is already being expanded up-stack (a recursive
	// typedef like map[string]Graph[T] re-enters through itself; an
	// alias chain of distinct typedefs unwinds freely).
	if bound || len(idxs) != len(bt.TParams) || i.expanding {
		return
	}
	if ue := bt.TypeExpr(); ue != nil {
		subs := map[string]ast.Expr{}
		for j, name := range bt.TParams {
			subs[name] = idxs[j]
		}
		// gc expands the pattern's named type exactly once: a named
		// instantiation nested in that underlying expr does not
		// re-expand, which also keeps a recursive typedef
		// (map[string]Graph[T]) from spinning forever.
		i.expanding = true
		defer func() { i.expanding = false }()
		i.UnifyDef(ctx, tset, binds, substTypeExprs(ue, subs), conc)
	}
}

// substTypeExprs replaces type-parameter names in a type expression —
// a local twin of compile's substTypeParams extended with FuncType so
// a func-typed underlying shape substitutes its signature too.
func substTypeExprs(e ast.Expr, subs map[string]ast.Expr) ast.Expr {
	switch t := e.(type) {
	case *ast.Ident:
		if a, ok := subs[t.Name]; ok {
			return a
		}
		return t
	case *ast.ParenExpr:
		return &ast.ParenExpr{Lparen: t.Lparen, X: substTypeExprs(t.X, subs), Rparen: t.Rparen}
	case *ast.ArrayType:
		return &ast.ArrayType{Lbrack: t.Lbrack, Len: t.Len, Elt: substTypeExprs(t.Elt, subs)}
	case *ast.MapType:
		return &ast.MapType{Map: t.Map, Key: substTypeExprs(t.Key, subs), Value: substTypeExprs(t.Value, subs)}
	case *ast.StarExpr:
		return &ast.StarExpr{Star: t.Star, X: substTypeExprs(t.X, subs)}
	case *ast.Ellipsis:
		return &ast.Ellipsis{Ellipsis: t.Ellipsis, Elt: substTypeExprs(t.Elt, subs)}
	case *ast.ChanType:
		return &ast.ChanType{Begin: t.Begin, Arrow: t.Arrow, Dir: t.Dir, Value: substTypeExprs(t.Value, subs)}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: t.X, Lbrack: t.Lbrack, Index: substTypeExprs(t.Index, subs), Rbrack: t.Rbrack}
	case *ast.IndexListExpr:
		idx := make([]ast.Expr, len(t.Indices))
		for i, x := range t.Indices {
			idx[i] = substTypeExprs(x, subs)
		}
		return &ast.IndexListExpr{X: t.X, Lbrack: t.Lbrack, Indices: idx, Rbrack: t.Rbrack}
	case *ast.FuncType:
		return &ast.FuncType{
			Func:    t.Func,
			Params:  substFieldList(t.Params, subs),
			Results: substFieldList(t.Results, subs),
		}
	}
	return e
}

func substFieldList(fl *ast.FieldList, subs map[string]ast.Expr) *ast.FieldList {
	if fl == nil {
		return nil
	}
	out := &ast.FieldList{Opening: fl.Opening, Closing: fl.Closing}
	for _, f := range fl.List {
		nf := *f
		nf.Type = substTypeExprs(f.Type, subs)
		out.List = append(out.List, &nf)
	}
	return out
}

// CoreTypes is Go's constraint type inference: a bound parameter whose
// constraint has a core type (`Map ~map[K]V`, `S ~[]E`) teaches the
// parameters that core type mentions — maps.Values(m) learns K and V
// from Map. It repeats while a round binds something new.
func (i *Inferer) CoreTypes(ctx *TypeDef, tset map[string]bool, binds map[string]Value, tparams []string, cons []ast.Expr) {
	for changed := true; changed; {
		changed = false
		for j, tp := range tparams {
			if j >= len(cons) {
				break
			}
			core := CoreTypeExpr(cons[j])
			if core == nil {
				continue
			}
			td := typedefOfBind(binds[tp])
			if td == nil {
				continue
			}
			u := i.underlying(td)
			if u == nil {
				continue
			}
			if u.Anon == nil && u.Spec != nil {
				// a declared container (`type M map[string]int`): unify
				// against its underlying type expression.
				u = &TypeDef{Kind: u.Kind, Anon: u.Spec.Type, Pkg: u.Pkg, File: u.File, Binds: u.Binds}
			}
			before := len(binds)
			i.UnifyDef(ctx, tset, binds, core, u)
			changed = changed || len(binds) > before
		}
	}
}

// CoreTypeExpr extracts the single composite type element of a
// constraint — `~[]E`, `[]E`, `interface{ ~map[K]V }` — or nil.
func CoreTypeExpr(c ast.Expr) ast.Expr {
	switch t := c.(type) {
	case *ast.ParenExpr:
		return CoreTypeExpr(t.X)
	case *ast.UnaryExpr:
		if t.Op == token.TILDE && IsCompositeTypeExpr(t.X) {
			return t.X
		}
	case *ast.InterfaceType:
		if t.Methods != nil && len(t.Methods.List) == 1 && len(t.Methods.List[0].Names) == 0 {
			return CoreTypeExpr(t.Methods.List[0].Type)
		}
	default:
		if IsCompositeTypeExpr(c) {
			return c
		}
	}
	return nil
}

// IsCompositeTypeExpr reports whether e spells a composite type
// literal — the shapes a `~` constraint may carry.
func IsCompositeTypeExpr(e ast.Expr) bool {
	switch e.(type) {
	case *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.ChanType, *ast.FuncType:
		return true
	}
	return false
}

// FuncTypeExpr returns the *ast.FuncType carried by a func-typed
// typedef, from its anonymous or declared form.
func FuncTypeExpr(td *TypeDef) *ast.FuncType {
	for _, x := range []ast.Expr{td.Anon, specTypeOfExpr(td)} {
		if ft, ok := x.(*ast.FuncType); ok {
			return ft
		}
	}
	return nil
}

func specTypeOfExpr(td *TypeDef) ast.Expr {
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// typedefOfBind extracts the typedef a bind value carries — *TypeDef
// directly, or through boxings Deref peels.
func typedefOfBind(v Value) *TypeDef {
	for {
		if td, ok := v.(*TypeDef); ok {
			return td
		}
		if _, isGV := v.(*GoValue); isGV {
			// a host value is the peel's leaf — dereferencing a *T
			// box would mint an endless chain of pointee boxes.
			return nil
		}
		dv, ok := Deref(v)
		if !ok {
			return nil
		}
		v = dv
	}
}
