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

// deferredLitTyp resolves a literal-type deferred fact: the site's Expr
// resolved, then Depth element peels, the map-key typedef when Key, and
// After more element peels — the container an elided literal or a
// literal key actually lands in.
func (v *VM) deferredLitTyp(f *frame, site *bytecode.CallSite) (*runtime.TypeDef, error) {
	td, err := v.deferredType(f, site)
	if err != nil || td == nil {
		return td, err
	}
	for i := 0; i < site.Depth && td != nil; i++ {
		td = v.deferredElem(f, td)
	}
	if site.Key && td != nil {
		td = v.mapKeyTypedef(f, td)
	}
	for i := 0; i < site.After && td != nil; i++ {
		td = v.deferredElem(f, td)
	}
	return td, nil
}

// deferredElem peels one container level; elemTypedef already resolves
// named types to their underlying container first (peelNamed).
func (v *VM) deferredElem(f *frame, td *runtime.TypeDef) *runtime.TypeDef {
	return v.elemTypedef(f, td)
}

// litKeyExprKey answers an OpLitKey's question: whether the resolved
// literal type is a map or an array/slice, where an ident key is a key
// expression — anything else makes the ident a field name.
func (v *VM) litKeyExprKey(f *frame, site *bytecode.CallSite) bool {
	td, err := v.deferredLitTyp(f, site)
	if err != nil {
		panic(&runtime.Trap{Pos: site.Pos, Reason: err.Error(), Err: err})
	}
	for td != nil {
		td = v.peelNamed(td)
		switch td.Kind {
		case runtime.KindMap, runtime.KindSlice:
			return true
		case runtime.KindPointer:
			// an elided `&T` literal reads keys by T's shape.
			td = v.elemTypedef(f, td)
		default:
			return false
		}
	}
	return false
}
