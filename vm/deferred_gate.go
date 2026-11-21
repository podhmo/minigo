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
	b, err := v.deferredResolve3(f, site, func(ctx *runtime.TypeDef) *deferredBox {
		b := &deferredBox{}
		if v.H.DeferredConst != nil {
			b.cnst, b.err = v.H.DeferredConst(site, ctx)
		} else {
			b.err = fmt.Errorf("OpConstExpr: no DeferredConst hook")
		}
		return b
	})
	if err != nil {
		return false, err
	}
	return b.cnst, nil
}

// deferredValueTyp answers an OpTypGate value side: the typedef of the
// site's value expression (`pkg.C` -> the member's declared type).
func (v *VM) deferredValueTyp(f *frame, site *bytecode.CallSite) (*runtime.TypeDef, bool, error) {
	b, err := v.deferredResolve3(f, site, func(ctx *runtime.TypeDef) *deferredBox {
		b := &deferredBox{}
		if v.H.DeferredValueTyp != nil {
			b.td, b.untyped, b.err = v.H.DeferredValueTyp(site, ctx)
		}
		return b
	})
	if err != nil {
		return nil, false, err
	}
	return b.td, b.untyped, nil
}

// typGate answers an OpTypGate: resolve both sides per the value mask
// and trap gc's mismatched-types wording when two named typedefs
// differ. A side that resolves to nil or an unnamed typedef stays
// silent — the compile gate only speaks on certainty.
func (v *VM) typGate(f *frame, siteA, siteB *bytecode.CallSite, mask int) {
	resolve := func(site *bytecode.CallSite, asValue bool) (*runtime.TypeDef, bool) {
		var td *runtime.TypeDef
		var untyped bool
		var err error
		if asValue {
			td, untyped, err = v.deferredValueTyp(f, site)
		} else {
			td, err = v.deferredType(f, site)
		}
		if err != nil {
			panic(&runtime.Trap{Pos: site.Pos, Reason: err.Error(), Err: err})
		}
		return td, untyped
	}
	tda, uta := resolve(siteA, mask&1 != 0)
	tdb, utb := resolve(siteB, mask&2 != 0)
	// mask bit2/bit3 mark an operand the compiler could not classify
	// under IndexedOnly; one resolving to an untyped constant adopts
	// the other side and stays out of the gate.
	if mask&4 != 0 && uta {
		tda = nil
	}
	if mask&8 != 0 && utb {
		tdb = nil
	}
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
				f.trap("invalid operation: mismatched types %s and untyped nil", tdNameIn(f.fn.Pkg, other))
			}
		}
	}
	if tda == nil || tdb == nil || tda.Name == "" || tdb.Name == "" {
		return
	}
	// an interface-typed operand never mismatches — whether the other
	// side is legal is assignability, which the comparison itself
	// decides (the static gate declines iface operands the same way).
	if v.ifaceTypedef(tda) || v.ifaceTypedef(tdb) {
		return
	}
	// a `type A = B` alias operand is the target type itself —
	// `var m os.FileMode` compares equal to an `fs.FileMode`
	// value, like gc.
	if !sameTypeDef(v.peelAlias(tda), v.peelAlias(tdb)) {
		f.trap("invalid operation: mismatched types %s and %s", tdNameIn(f.fn.Pkg, tda), tdNameIn(f.fn.Pkg, tdb))
	}
}

// ifaceTypedef reports whether td's underlying type is an interface.
func (v *VM) ifaceTypedef(td *runtime.TypeDef) bool {
	u := v.peelNamed(td)
	return u != nil && u.Kind == runtime.KindInterface
}
