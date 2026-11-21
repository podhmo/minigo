package vm

import (
	"go/ast"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// hostMethodSigMatch compares a signature-bearing requirement against
// the reflect method on a host box — reflect methods carry no declared
// signature for dynFns, so without this their name alone satisfied any
// same-named requirement regardless of signature. Non-host values and
// unspellable requirements keep the name-only pass.
func (v *VM) hostMethodSigMatch(reqFn *runtime.Function, m string, x runtime.Value) bool {
	u := x
	if d, ok := runtime.Deref(u); ok {
		u = d
	}
	gv, ok := runtime.Unwrap(u).(*runtime.GoValue)
	if !ok {
		return true
	}
	rv := hostbridge.RVOf(gv.V)
	if !rv.IsValid() {
		return true
	}
	hm := rv.MethodByName(m)
	if !hm.IsValid() {
		return true // the name pass already decided presence
	}
	sig, pkg, file, binds := runtime.FuncSigOf(reqFn)
	if sig == nil {
		return true
	}
	mt := hm.Type()
	params, results := sigExprs(sig.Params), sigExprs(sig.Results)
	if len(params) != mt.NumIn() || len(results) != mt.NumOut() {
		return false
	}
	// signature identity covers variadicness too: `Write([]byte)` does
	// not satisfy `Write(...byte)` and vice versa.
	varReq := false
	if sig.Params != nil && len(sig.Params.List) > 0 {
		_, varReq = sig.Params.List[len(sig.Params.List)-1].Type.(*ast.Ellipsis)
	}
	if varReq != mt.IsVariadic() {
		return false
	}
	from := &runtime.TypeDef{Pkg: pkg, File: file, Binds: binds}
	for i, e := range params {
		if !v.sigTypeFitsHost(from, e, mt.In(i)) {
			return false
		}
	}
	for i, e := range results {
		if !v.sigTypeFitsHost(from, e, mt.Out(i)) {
			return false
		}
	}
	return true
}

// sigExprs flattens a field list to one type expr per position —
// `func(a, b int)` yields int twice, and a variadic `...T` spells its
// element as the slice the reflect signature carries.
func sigExprs(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		t := f.Type
		if el, ok := t.(*ast.Ellipsis); ok {
			t = &ast.ArrayType{Lbrack: el.Pos(), Elt: el.Elt}
		}
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for j := 0; j < n; j++ {
			out = append(out, t)
		}
	}
	return out
}

// sigTypeFitsHost reports whether a signature position's declared type
// expr, resolved in its declaring context, matches the reflect type a
// host method carries. Spellings miss what resolution knows: an alias
// (`type S = string` spells S where the host spells string), and a
// facade's method results spell its concrete type where the interface
// names the public one (`Elem() reflect.Type` vs `Elem() *RType`).
// Unresolvable positions stay permissive — the name pass already
// decided presence.
func (v *VM) sigTypeFitsHost(from *runtime.TypeDef, e ast.Expr, rt hostbridge.Type) bool {
	// Composite type exprs compare structurally: ResolveType peels a
	// `*` to the element's typedef, so `Equal(*T)` would look like
	// `Equal(T)` if the leaf typedef answered for the whole expr. The
	// structural walk only applies to unnamed host types — a named
	// host type (`net.IP` over []byte) is identical only to its own
	// spelling, so it falls through to the leaf compare.
	switch t := e.(type) {
	case *ast.ParenExpr:
		return v.sigTypeFitsHost(from, t.X, rt)
	}
	if rt.Name() != "" {
		switch e.(type) {
		case *ast.StarExpr, *ast.ArrayType, *ast.MapType, *ast.ChanType:
			// a composite spelling never names a named type —
			// `*string` is not `string` even though ResolveType
			// would peel the `*` and answer the element typedef.
			return false
		}
	} else {
		switch t := e.(type) {
		case *ast.StarExpr:
			return rt.Kind() == hostbridge.KindPointer && v.sigTypeFitsHost(from, t.X, rt.Elem())
		case *ast.ArrayType:
			if t.Len == nil {
				return rt.Kind() == hostbridge.KindSlice && v.sigTypeFitsHost(from, t.Elt, rt.Elem())
			}
			if rt.Kind() != hostbridge.KindArray {
				return false
			}
			// `[16]byte` never satisfies `[4]byte`: type identity
			// includes the constant length. A length still unfolded
			// keeps the element-only compare — the name pass already
			// decided presence.
			if n, ok := runtime.FoldedArrayLen(from.Pkg, t); ok && int64(rt.Len()) != n {
				return false
			}
			return v.sigTypeFitsHost(from, t.Elt, rt.Elem())
		case *ast.MapType:
			return rt.Kind() == hostbridge.KindMap &&
				v.sigTypeFitsHost(from, t.Key, rt.Key()) &&
				v.sigTypeFitsHost(from, t.Value, rt.Elem())
		case *ast.ChanType:
			dir := hostbridge.KindBothDir
			if t.Dir == ast.SEND {
				dir = hostbridge.KindSendDir
			} else if t.Dir == ast.RECV {
				dir = hostbridge.KindRecvDir
			}
			return rt.Kind() == hostbridge.KindChan && rt.ChanDir() == dir &&
				v.sigTypeFitsHost(from, t.Value, rt.Elem())
		}
	}
	td, err := v.H.ResolveType(from, e)
	if err != nil || td == nil {
		// `any` is not in the basic-name table so the resolver may not
		// answer — but it is a real type for signature identity: the
		// host position must itself be the empty interface. A declared
		// `type any = T` resolves above and never reaches this.
		if id, ok := e.(*ast.Ident); ok && id.Name == "any" {
			return rt.Kind() == hostbridge.KindInterface && rt.NumMethod() == 0
		}
		return true
	}
	td = v.peelAlias(td)
	if td.HostNew != nil {
		ht := td.HostType()
		if rt == ht || rt.AssignableTo(ht) {
			return true
		}
		// a facade boxes its zero as *T, but a signature position can
		// legitimately carry T itself — e.g. the element of a declared
		// *T after the composite walk peeled the star.
		return ht.Kind() == hostbridge.KindPointer && rt == ht.Elem()
	}
	if td.Kind == runtime.KindInterface {
		if len(td.MReqs) == 0 {
			// `any`/`interface{}` is a type, not a wildcard — the
			// host position must itself be the empty interface.
			return rt.Kind() == hostbridge.KindInterface && rt.NumMethod() == 0
		}
		// name-level method-set cover: a host result spelling the
		// facade's concrete type (*RType) still satisfies reflect.Type.
		for _, name := range td.MReqs {
			if _, ok := rt.MethodByName(name); !ok {
				return false
			}
		}
		return true
	}
	return rt.String() == runtime.TypGoSpelling(e, from) ||
		(td.Name != "" && rt.String() == td.Name)
}
