package vm

import (
	"fmt"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// deferredBox caches one CallSite's resolution outcome. The first
// execution of an OpDeferredTyp writes it; later executions — and other
// goroutines running the same chunk — reuse it, so a Missing/Failed
// answer traps identically every time.
type deferredBox struct {
	td  *runtime.TypeDef
	err error
}

// deferredType answers a deferred fact (M4-7): the compiler emitted a
// CallSite instead of a typedef when the question needed AllowIndex. The
// resolution context is the site's own Ctx when the producer pinned one,
// else the running frame's package, file and instantiation binds — the
// same context the compiler resolved against.
func (v *VM) deferredType(f *frame, site *bytecode.CallSite) (*runtime.TypeDef, error) {
	if b, ok := site.Cache.Load().(*deferredBox); ok {
		return b.td, b.err
	}
	ctx, _ := site.Ctx.(*runtime.TypeDef)
	if ctx == nil {
		ctx = &runtime.TypeDef{Pkg: f.fn.Pkg, File: site.File, Binds: f.fn.Binds}
	}
	b := &deferredBox{}
	if v.H.ResolveType == nil {
		b.err = fmt.Errorf("OpDeferredTyp: no ResolveType hook")
	} else {
		b.td, b.err = v.H.ResolveType(ctx, site.Expr)
	}
	// a lost race leaves an equivalent box cached; both answers are the
	// same type fact, so either winner is correct.
	site.Cache.Store(b)
	return b.td, b.err
}
