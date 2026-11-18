package vm

import (
	"fmt"
	"sync"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// deferredBox caches one CallSite resolution outcome: the typedef on
// success, or the failure the next execution re-raises.
type deferredBox struct {
	td  *runtime.TypeDef
	err error
}

// deferredCache is a CallSite's memo. A chunk is shared across a
// generic function's instantiations (Function.WithBinds copies the
// descriptor, not the code), so a site whose context comes from the
// running frame is keyed by its instantiation binds; a site with a
// producer-pinned context caches under the empty key.
type deferredCache struct {
	mu    sync.Mutex
	byKey map[string]*deferredBox
}

// deferredType answers a deferred fact (M4-7): the compiler emitted a
// CallSite instead of a typedef when the question needed AllowIndex. The
// resolution context is the site's own Ctx when the producer pinned one,
// else the running frame's package, file and instantiation binds — the
// same context the compiler resolved against.
func (v *VM) deferredType(f *frame, site *bytecode.CallSite) (*runtime.TypeDef, error) {
	ctx, pinned := site.Ctx.(*runtime.TypeDef)
	key, cacheable := "", true
	if !pinned {
		ctx = &runtime.TypeDef{Pkg: f.fn.Pkg, File: site.File, Binds: f.fn.Binds}
		if len(ctx.Binds) > 0 {
			if key, cacheable = v.instBindsKey(ctx.Binds); !cacheable {
				// a bind without a type identity can't key the memo —
				// resolving per call beats answering for the wrong
				// instantiation.
				key = ""
			}
		}
	}
	resolve := func() *deferredBox {
		b := &deferredBox{}
		switch {
		case v.H.DeferredType != nil:
			b.td, b.err = v.H.DeferredType(site, ctx)
		case v.H.ResolveType != nil:
			b.td, b.err = v.H.ResolveType(ctx, site.Expr)
		default:
			b.err = fmt.Errorf("OpDeferredTyp: no DeferredType hook")
		}
		return b
	}
	if !cacheable {
		b := resolve()
		return b.td, b.err
	}
	c, _ := site.Cache.Load().(*deferredCache)
	if c == nil {
		c = &deferredCache{byKey: map[string]*deferredBox{}}
		// a lost install race leaves an equivalent cache; the losing
		// goroutine resolves once more and stores under the winner.
		site.Cache.Store(c)
	}
	c.mu.Lock()
	b := c.byKey[key]
	c.mu.Unlock()
	if b == nil {
		b = resolve()
		c.mu.Lock()
		if prev := c.byKey[key]; prev != nil {
			b = prev
		} else {
			c.byKey[key] = b
		}
		c.mu.Unlock()
	}
	return b.td, b.err
}
