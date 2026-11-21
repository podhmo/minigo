package compile

import (
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
)

// memberFact is what the compiler can know about a package member: the
// declaring TypeSpec for a source-indexed type decl, or the bound
// runtime typedef for a host-bound member — plus the package the answer
// came from (a spec's inner names resolve in its own file scope, never
// the caller's).
type memberFact struct {
	pkg  *runtime.Package
	spec *ast.TypeSpec
	td   *runtime.TypeDef
}

// selMember resolves a package member name to what the compiler can
// know about it, as a semantics.Result so the fact query's kind
// reaches the type classification (M2-4/M4-7): Found when an indexed
// package answered, Deferred when the DeferFacts knob kept a
// materialized Val (the out-of-band question still answers) — an
// unindexed ref returns Deferred with Val nil, Missing when an indexed
// package has no such member, Failed on a load error.
// semantics: fact — selIsType, selIsIface and selNilable all read this.
func (c *compiler) selMember(ref *runtime.ImportRef, name string) semantics.Result[*memberFact] {
	res := c.askIndexed(ref)
	if res.Val == nil {
		// Missing, Failed — and Deferred without a materialized
		// package — all mean the oracle cannot answer here; the
		// classification defers rather than answering false.
		return semantics.Result[*memberFact]{Kind: res.Kind, Err: res.Err}
	}
	p := res.Val
	answer := func(f *memberFact) semantics.Result[*memberFact] {
		f.pkg = p
		return semantics.Result[*memberFact]{Kind: res.Kind, Val: f}
	}
	if p.Index != nil {
		if tdi := p.Index.Types[name]; tdi != nil {
			if tdi.Decl != nil {
				if ts, ok := tdi.Decl.Spec.(*ast.TypeSpec); ok {
					return answer(&memberFact{spec: ts})
				}
			}
			// a synthetic entry (a method receiver whose declaration
			// lives outside this index) still names a type — keep the
			// old ix.Types[name] != nil verdict even with no spec to
			// hand back; the globals lookup may still find its
			// materialized typedef. Only an already-indexed package
			// reaches this: the unindexed Deferred above carried no
			// Val and returned without answering.
			if p.Globals != nil {
				if v, ok := p.Globals.Get(name); ok {
					if td, ok := v.(*runtime.TypeDef); ok && td != nil {
						return answer(&memberFact{td: td})
					}
				}
			}
			return answer(&memberFact{})
		}
	}
	if p.Globals != nil {
		if v, ok := p.Globals.Get(name); ok {
			if td, ok := v.(*runtime.TypeDef); ok && td != nil {
				return answer(&memberFact{td: td})
			}
		}
	}
	return semantics.Result[*memberFact]{Kind: semantics.Missing}
}

// selIsType reports whether the name a qualified expression picks out
// of an imported package names a type: a bound typedef in the
// package's globals, or a type declaration in its source index.
// Functions, variables, constants and unresolved references report
// false — `pkg.fn(x)` then keeps its meaning as a real call.
func (c *compiler) selIsType(ref *runtime.ImportRef, name string) bool {
	return c.selMember(ref, name).Val != nil
}

// selIsIface reports whether the name a qualified type expression picks
// out of an imported package names an interface: a bound typedef's
// KindInterface flag, or an InterfaceType spec in a source-indexed
// package. Unknown references and unresolved names report false.
func (c *compiler) selIsIface(ref *runtime.ImportRef, name string) bool {
	f := c.selMember(ref, name).Val
	if f == nil {
		return false
	}
	if f.spec != nil {
		_, isIface := ast.Unparen(f.spec.Type).(*ast.InterfaceType)
		return isIface
	}
	return f.td != nil && f.td.Kind == runtime.KindInterface
}

// semantics: fact — decides whether `x == nil` on pkg.T traps the way
// gc rejects it (interfaces/pointers/slices/maps/chans/funcs are
// nilable; structs and basic types are not).
//
// selNilable reports whether the type a qualified name picks out of an
// imported package can be nil: interfaces, pointers, slices, maps,
// chans and funcs can; structs and basic types cannot. ok=false when
// the member can't be resolved at all — the caller defers the nil
// check to run time instead of guessing nilable.
func (c *compiler) selNilable(ref *runtime.ImportRef, name string) (bool, bool) {
	res := c.selMember(ref, name)
	if res.Kind != semantics.Found || res.Val == nil {
		return false, false
	}
	f := res.Val
	if f.spec != nil {
		// the spec's inner names resolve in ITS package's file scope —
		// `type T = otherpkg.Nilable` must read otherpkg through the
		// spec's own imports, never the caller's, or a same-named
		// import there would flip the verdict.
		n, _ := c.nilableTypExpr(c.unfoldSpecType(f.pkg, f.spec, 4))
		return n, true
	}
	if f.td == nil {
		// a type name with no spec or typedef in view (synthetic
		// index entry): unknown shape, so conservatively non-nilable.
		return false, true
	}
	switch f.td.Kind {
	case runtime.KindInterface, runtime.KindPointer, runtime.KindSlice,
		runtime.KindMap, runtime.KindChan, runtime.KindFunc:
		return true, true
	}
	if f.td.HostNew != nil {
		// a bound host type (hostType) is classed a struct whatever
		// its shape; its zero's own kind tells a named slice or map
		// (net.IP, net.HardwareAddr) that answers `== nil`. A struct
		// binds a pointer zero, which is no evidence of nilability.
		switch f.td.HostType().Kind() {
		case reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
			return true, true
		}
	}
	// a declared type's spec still spells its shape — `type List []T`
	// materializes with the declared kind, not the slice's, and the
	// Kind switch above reports it non-nilable. Read the spec expr,
	// unfolded under the decl's own file scope.
	if f.td.Anon != nil {
		n, _ := c.nilableTypExpr(c.unfoldScopedTypExpr(f.td.Anon, f.td.File, 4))
		return n, true
	}
	return false, true
}
