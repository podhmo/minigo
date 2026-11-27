package vm

// freeFrame returns a frame to the pool after clearing every field:
// stale values left in the retained buffers would pin dead script
// objects across the frame's next life. Only frames that ended without
// a panic in flight may be pooled — a panicking frame can still be
// listed in v.unwinding for the live traceback.
func freeFrame(f *frame) {
	f.fn = nil
	f.ch = nil
	f.upvals = nil
	for i := range f.locals {
		f.locals[i] = nil
	}
	f.locals = f.locals[:0]
	for i := range f.ltyps {
		f.ltyps[i] = nil
	}
	f.ltyps = f.ltyps[:0]
	// keep the len so the clear drops every wrapper's cell pointer —
	// truncating first would pin the cells a recycled frame still
	// references through the stale tail.
	clear(f.slotRefs)
	// re-slice before clearing so a stale view (the arena may have
	// grown past this backing) clears the live window.
	if f.v != nil {
		f.stack = f.v.vals[f.base : f.base+f.sp]
		clear(f.stack)
		// drop the arena tail only when this window is the top — a
		// suspended frame's region below stays live.
		if len(f.v.vals) == f.base+f.sp {
			f.v.vals = f.v.vals[:f.base]
		}
	}
	f.stack = nil
	f.valsCap = 0
	f.base, f.sp = 0, 0
	f.tailParked = false
	// drop the VM link — a pooled frame must not pin its goroutine's
	// arena, hooks, or the session behind them.
	f.v = nil
	f.ip = 0
	for i := range f.defers {
		f.defers[i] = deferredCall{}
	}
	f.defers = f.defers[:0]
	f.deferredTrap = nil
	f.sentinel = false
	f.retNamed = false
	// f.results' backing may be the returned Tuple's Elems — release it
	// rather than clearing (OpReturn allocates a fresh slice per call).
	f.results = nil
	f.res1[0] = nil
	f.boundLo, f.boundHi = 0, 0
	f.vpack = nil
	if f.cellsShared || f.cellsLeaked {
		f.cells = nil
		f.cellsShared = false
		f.cellsLeaked = false
	} else {
		clear(f.cells)
	}
	f.cellsAligned = false
	framePool.Put(f)
}
