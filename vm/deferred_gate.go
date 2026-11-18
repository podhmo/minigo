package vm

import (
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// deferredConst answers an OpConstExpr site: is the site's expression a
// constant expression under the site's context.
func (v *VM) deferredConst(f *frame, site *bytecode.CallSite) (bool, error) {
	_, cnst, err := v.deferredResolve3(f, site, func(ctx *runtime.TypeDef) *deferredBox {
		b := &deferredBox{}
		if v.H.DeferredConst != nil {
			b.cnst, b.err = v.H.DeferredConst(site, ctx)
		} else {
			b.err = fmt.Errorf("OpConstExpr: no DeferredConst hook")
		}
		return b
	})
	return cnst, err
}

// deferredValueTyp answers an OpTypGate value side: the typedef of the
// site's value expression (`pkg.C` -> the member's declared type).
func (v *VM) deferredValueTyp(f *frame, site *bytecode.CallSite) (*runtime.TypeDef, error) {
	td, _, err := v.deferredResolve3(f, site, func(ctx *runtime.TypeDef) *deferredBox {
		b := &deferredBox{}
		if v.H.DeferredValueTyp != nil {
			b.td, b.err = v.H.DeferredValueTyp(site, ctx)
		}
		return b
	})
	return td, err
}

// typGate answers an OpTypGate: resolve both sides per the value mask
// and trap gc's mismatched-types wording when two named typedefs
// differ. A side that resolves to nil or an unnamed typedef stays
// silent — the compile gate only speaks on certainty.
func (v *VM) typGate(f *frame, siteA, siteB *bytecode.CallSite, mask int) {
	resolve := func(site *bytecode.CallSite, asValue bool) *runtime.TypeDef {
		var td *runtime.TypeDef
		var err error
		if asValue {
			td, err = v.deferredValueTyp(f, site)
		} else {
			td, err = v.deferredType(f, site)
		}
		if err != nil {
			panic(&runtime.Trap{Pos: site.Pos, Reason: err.Error(), Err: err})
		}
		return td
	}
	tda := resolve(siteA, mask&1 != 0)
	tdb := resolve(siteB, mask&2 != 0)
	// a `nil` operand resolves to no typedef at all — but it still
	// gates: against a resolved non-nilable named type gc says
	// `mismatched types T and untyped nil`.
	for i, site := range [2]*bytecode.CallSite{siteA, siteB} {
		if mask&(1<<i) == 0 {
			continue
		}
		if id, ok := site.Expr.(*ast.Ident); ok && id.Name == "nil" {
			other := tda
			if i == 0 {
				other = tdb
			}
			if other != nil && other.Name != "" && !v.nilableTypedef(other) {
				f.trap("invalid operation: mismatched types %s and untyped nil", tdName(other))
			}
		}
	}
	if tda == nil || tdb == nil || tda.Name == "" || tdb.Name == "" {
		return
	}
	if !sameTypeDef(tda, tdb) {
		f.trap("invalid operation: mismatched types %s and %s", tdName(tda), tdName(tdb))
	}
}
