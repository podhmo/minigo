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
	gv, ok := runtime.Unwrap(x).(*runtime.GoValue)
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
	td, err := v.H.ResolveType(from, e)
	if err != nil || td == nil {
		return true
	}
	td = v.peelAlias(td)
	if td.HostNew != nil {
		ht := td.HostType()
		return rt == ht || rt.AssignableTo(ht)
	}
	if td.Kind == runtime.KindInterface {
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
