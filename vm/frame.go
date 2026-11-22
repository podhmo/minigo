// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"go/token"
	"sync"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/spike"
	"github.com/podhmo/minigo/runtime"
)

// maxFrames bounds the call stack; exceeding it traps instead of letting
// unbounded script recursion blow the host goroutine stack (a fatal,
// untraceable crash in Go) — VM frames do consume host stack.
const maxFrames = 10000

type deferredCall struct {
	fn   runtime.Value
	args []runtime.Value
	pos  token.Pos
	// typs carries the call site's resolved arg facts — registration
	// resolves the site's ArgHint recipes eagerly because the calling
	// frame they read is gone by the time the deferred call runs.
	typs callTyps
}

// callTyps bundles the type facts one call site resolved: the
// per-argument declared typedefs — inference binds T to the
// argument's static type the same way an immediate OpCall does —
// and the element typedef a trailing `xs...` argument carried into
// the call (`Sum(n...)`'s []int when the nil slice expanded to zero
// arguments).
type callTyps struct {
	statics  []*runtime.TypeDef
	spreadTd *runtime.TypeDef
}

type frame struct {
	fn *runtime.Function
	ch *bytecode.Chunk
	// The operand stack is a window on the goroutine's shared arena
	// (v.vals): vals[base:base+sp]. stack caches that view — re-sliced
	// by every mutator; a bare read on a stale view still sees the
	// frame's own operands because nothing but this frame writes its
	// window, but every write or length change must go through the
	// mutators so a growth-induced reallocation cannot lose it.
	v     *VM
	base  int
	sp    int
	stack []runtime.Value
	// valsCap is cap(v.vals) at the last stack re-cut. A nested call
	// growing the arena past its capacity moves v.vals to a new
	// backing — cap differs, marking this frame's cached view stale:
	// reads still see its own operands (nothing else writes the
	// window), but writes must re-cut through refresh first. The
	// dispatch loop pays one compare per instruction instead of a
	// re-slice; mutators that re-cut keep the stamp current.
	valsCap int
	// tailParked marks a suspended frame whose window a yield's park
	// copied out of the arena — the matching restore shifts it back.
	tailParked bool
	// locals is the slot array: a *localCell wrapper for every slot the
	// compiler marked boxed (P4-2 — refs, captures, ptr receivers,
	// special-form visibility) or that promoted itself at run time, and
	// the slot's raw value for plain slots, which never need a cell
	// object. The wrapper is required: a plain slot's value may itself
	// be a *runtime.Cell (a ref from &x), so the slot kind cannot ride
	// on the element's type. ltyps carries a plain slot's declared
	// typedef (OpCoerce stamps it); boxed slots keep Typ on the cell.
	locals []runtime.Value
	ltyps  []*runtime.TypeDef
	// slotRefs is the frame-recyclable block localCell wrappers come
	// from — wrappers never leave the frame (captures take the *Cell
	// they hold), so the block recycles even when cells must not.
	slotRefs []localCell
	upvals   []*runtime.Cell
	ip       int

	defers   []deferredCall // LIFO
	sentinel bool           // placeholder frame for a deferred builtin — a
	// wrapper in Go's terms: it does not count toward recover()'s
	// one-frame distance rule
	retNamed bool // gather named result slots after defers run
	results  []runtime.Value
	// res1 backs f.results for the single-result return — the slice
	// header points at the frame's own scratch instead of a fresh
	// backing array, so `return x` allocates nothing. Only n==1 may
	// use it: a multi-result slice escapes as the returned Tuple's
	// Elems and must not share the pooled frame's storage.
	res1 [1]runtime.Value

	// boundLo/boundHi bound a re-entrant loop run (range-over-func yield):
	// the bounded run executes only while boundLo < ip < boundHi and halts
	// on either edge — the loop's back-edge address is boundLo, its exit
	// boundHi. boundHi == 0 disables bounding (normal execution).
	boundLo int
	boundHi int

	// selSeen marks the refs currently on a selectMember refollow path:
	// a cyclic pointer (x := any(nil); x = &x) refollows forever.
	selSeen map[runtime.Value]bool

	// vpack is the slice prepFrame packed a variadic call's trailing
	// arguments into (`f(1, 2)` for `func f(xs ...T)`): the parameter's
	// coerce converts its elements to T — they arrive as raw call
	// arguments, untyped constants included. A spread `f(xs...)` binds
	// the caller's slice itself and leaves vpack nil.
	vpack *runtime.Slice

	// cells is the block backing locals when the chunk cannot leak its
	// cells (chunkCellsEscape false): prepFrameSeen refills it each call
	// and freeFrame recycles it. cellsShared marks that specialCtx.Eval
	// lent this frame's cells to a nested eval frame — their escape can
	// no longer be ruled out, so freeFrame drops the block.
	cells       []runtime.Cell
	cellsShared bool
	// cellsAligned marks the pooled block laid out one cell per slot
	// (every chunk now): a promoted plain slot takes cells[i] instead
	// of allocating.
	cellsAligned bool
	// cellsLeaked marks that a pooled cell was actually published this
	// call (OpLocalRef's non-transient push, a closure capture) —
	// freeFrame drops the block like cellsShared. Publishing a slot's
	// own heap cell (a post-publish rebind allocated it) does not set
	// the flag: the block itself still never escaped.
	cellsLeaked bool
}

// framePool recycles frames after a normal return. The frame object
// and its flat scratch (operand stack, call arg buffers, the locals
// pointer array, and — for chunks that cannot leak them — the cells
// block) survive; everything else is freshly allocated per call.
var framePool = sync.Pool{New: func() any { return &frame{} }}

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

// chunkCellsEscape reports whether the chunk's local cells can outlive
// the frame: an OpLocalRef pushes the cell itself as a script value,
// and an OpMakeClosure whose literal captures a local slot (rather than
// only parent upvals) keeps the cell in the closure's upvals. A chunk
// the compiler classified (Boxed non-nil) answers from the recorded
// per-slot classes instead — stores and reads hold their refs
// transiently, so only boxed slots escape. The memo lives on the chunk
// (Escapes: 0 uncomputed, 1 bound, 2 escaping).
func chunkCellsEscape(ch *bytecode.Chunk) bool {
	if e := ch.Escapes.Load(); e != 0 {
		return e == 2
	}
	esc := false
	if ch.Boxed != nil {
		for _, b := range ch.Boxed {
			if b {
				esc = true
				break
			}
		}
	} else {
	scan:
		for _, ins := range ch.Code {
			switch ins.Op {
			case bytecode.OpLocalRef:
				esc = true
				break scan
			case bytecode.OpMakeClosure:
				if proto, ok := ch.Consts[ins.A].(*runtime.Function); ok && proto.Chunk != nil {
					for _, d := range proto.Chunk.Upvals {
						if !d.FromParentUpval {
							esc = true
							break scan
						}
					}
				}
			}
		}
	}
	spike.NoteChunkEscape(esc)
	if esc {
		ch.Escapes.Store(2)
	} else {
		ch.Escapes.Store(1)
	}
	return esc
}

// localCell marks a boxed slot inside frame.locals — an unexported
// wrapper a script value can never impersonate, which is what tells a
// boxed slot apart from a plain slot storing a *runtime.Cell value.
type localCell struct{ c *runtime.Cell }

// boxedCell returns the cell backing slot i when the slot is boxed.
func (f *frame) boxedCell(i int) (*runtime.Cell, bool) {
	if lc, ok := f.locals[i].(*localCell); ok {
		return lc.c, true
	}
	return nil, false
}

// setLocalCell marks slot i boxed, pointing it at c.
func (f *frame) setLocalCell(i int, c *runtime.Cell) { f.locals[i] = &localCell{c} }

// localVal reads slot i's value — a boxed slot's cell element or a
// plain slot's stored value.
func (f *frame) localVal(i int) runtime.Value {
	if c, ok := f.boxedCell(i); ok {
		return c.Elem
	}
	return f.locals[i]
}

// localCell returns the cell backing slot i for a ref or capture that
// can publish it, promoting a plain slot when a ref demands
// addressability the compiler's classification missed. Handing out a
// cell inside the pooled block flags cellsLeaked — the block must outlive
// the frame — while a slot rebound onto its own heap cell publishes
// nothing pooled.
func (f *frame) localCell(i int) *runtime.Cell {
	if c, ok := f.boxedCell(i); ok {
		if f.cellsAligned && i < len(f.cells) && c == &f.cells[i] {
			f.cellsLeaked = true
		}
		return c
	}
	if f.cellsAligned && i < len(f.cells) && i < len(f.slotRefs) {
		c := &f.cells[i]
		c.Elem = f.locals[i]
		if i < len(f.ltyps) {
			c.Typ = f.ltyps[i]
		}
		f.slotRefs[i].c = c
		f.locals[i] = &f.slotRefs[i]
		f.cellsLeaked = true
		spike.NoteSlotPromoted()
		return c
	}
	c := &runtime.Cell{Elem: f.locals[i]}
	if i < len(f.ltyps) {
		c.Typ = f.ltyps[i]
	}
	spike.NoteSlotPromoted()
	f.setLocalCell(i, c)
	return c
}

// localCellTransient is localCell for a ref the compiler proved
// transient (a store target): the promoted cell cannot be published,
// so on a non-escaping chunk it comes from the frame's pooled block —
// the cell and its wrapper recycle with the frame and a promotion
// allocates nothing.
func (f *frame) localCellTransient(i int) *runtime.Cell {
	if c, ok := f.boxedCell(i); ok {
		return c
	}
	if f.cellsAligned && i < len(f.cells) && i < len(f.slotRefs) {
		c := &f.cells[i]
		c.Elem = f.locals[i]
		if i < len(f.ltyps) {
			c.Typ = f.ltyps[i]
		}
		f.slotRefs[i].c = c
		f.locals[i] = &f.slotRefs[i]
		spike.NoteSlotPromoted()
		return c
	}
	return f.localCell(i)
}

// localTyp returns slot i's declared typedef — the cell's stamp for a
// boxed slot, the ltyps entry for a plain one.
func (f *frame) localTyp(i int) *runtime.TypeDef {
	if c, ok := f.boxedCell(i); ok {
		return c.Typ
	}
	if i < len(f.ltyps) {
		return f.ltyps[i]
	}
	return nil
}

// putLocal stores into slot i without coercion — the named-result
// writeback and parameter binding write the value as is.
func (f *frame) putLocal(i int, x runtime.Value) {
	if c, ok := f.boxedCell(i); ok {
		c.Elem = x
	} else {
		f.locals[i] = x
	}
}

// push appends one operand. The reslice to f.base+f.sp keeps len(v.vals)
// tracking this window's top: any dead tail above (an unwound frame's
// leftovers) is dropped, never written over.
func (f *frame) push(x runtime.Value) {
	v := f.v
	v.vals = append(v.vals[:f.base+f.sp], x)
	f.sp++
	f.stack = v.vals[f.base:]
	f.valsCap = cap(v.vals)
}

func (f *frame) pop() runtime.Value {
	// atTail before the decrement: the arena top equals this window's
	// end only while no suspended frame's region sits above.
	atTail := f.base+f.sp == len(f.v.vals)
	x := f.stack[f.sp-1]
	// nil the slot before dropping it — the backing arena keeps the
	// region past len, and a dead script value pinned there outlives
	// the pop by a whole session otherwise.
	f.stack[f.sp-1] = nil
	f.sp--
	f.stack = f.stack[:f.sp]
	if atTail {
		f.v.vals = f.v.vals[:f.base+f.sp]
	}
	return x
}

// setSp resizes the window — the in-loop form of a multi-pop.
func (f *frame) setSp(n int) {
	// clear the dropped slots first — like pop, the arena keeps the
	// region past len and would pin dead operands in it.
	clear(f.stack[n:f.sp])
	f.sp = n
	f.stack = f.v.vals[f.base : f.base+n]
	f.valsCap = cap(f.v.vals)
	if f.base+n < len(f.v.vals) {
		f.v.vals = f.v.vals[:f.base+n]
	}
}

// refresh re-slices the cached window view — needed before a bare
// f.stack write when a nested call may have grown the arena past this
// frame's backing array.
func (f *frame) refresh() {
	f.stack = f.v.vals[f.base : f.base+f.sp]
	f.valsCap = cap(f.v.vals)
}

// stkSet writes operand i — a bare f.stack[i] write could land in a
// stale backing array after a nested call grew the arena.
func (f *frame) stkSet(i int, x runtime.Value) {
	f.refresh()
	f.stack[i] = x
}

func (f *frame) pos() token.Pos {
	if f.ip > 0 {
		return f.ch.Code[f.ip-1].Pos
	}
	return 0
}

func (f *frame) trap(format string, args ...any) {
	panic(&runtime.Trap{Pos: f.pos(), Reason: fmt.Sprintf(format, args...)})
}

func (v *VM) framesPop() { v.frames = v.frames[:len(v.frames)-1] }
