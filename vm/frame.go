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
	// statics holds the call site's per-argument declared typedefs —
	// inference binds T to the argument's static type at deferred call
	// time the same way an immediate OpCall does.
	statics []*runtime.TypeDef
	// spreadTd is the element typedef a trailing `xs...` argument carried
	// into the call — type inference still sees `Sum(n...)`'s []int when
	// the nil slice expanded to zero arguments.
	spreadTd *runtime.TypeDef
}

type frame struct {
	fn *runtime.Function
	ch *bytecode.Chunk
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
	stack    []runtime.Value
	ip       int

	defers   []deferredCall // LIFO
	sentinel bool           // placeholder frame for a deferred builtin — a
	// wrapper in Go's terms: it does not count toward recover()'s
	// one-frame distance rule
	retNamed bool // gather named result slots after defers run
	results  []runtime.Value

	// argbuf/statbuf are OpCall's reusable arg/static scratch (see
	// popArgsCall): a frame issuing millions of calls otherwise allocates
	// two slices per call.
	argbuf  []runtime.Value
	statbuf []*runtime.TypeDef

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
	clear(f.stack)
	f.stack = f.stack[:0]
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
	clear(f.argbuf)
	f.argbuf = f.argbuf[:0]
	clear(f.statbuf)
	f.statbuf = f.statbuf[:0]
	f.boundLo, f.boundHi = 0, 0
	f.vpack = nil
	if f.cellsShared {
		f.cells = nil
		f.cellsShared = false
	} else {
		clear(f.cells)
	}
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

// localCell returns the cell backing slot i, promoting a plain slot's
// value into a fresh cell in place when a real ref or capture demands
// addressability the compiler's classification missed.
func (f *frame) localCell(i int) *runtime.Cell {
	if c, ok := f.boxedCell(i); ok {
		return c
	}
	c := &runtime.Cell{Elem: f.locals[i]}
	if i < len(f.ltyps) {
		c.Typ = f.ltyps[i]
	}
	f.setLocalCell(i, c)
	return c
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

func (f *frame) push(v runtime.Value) { f.stack = append(f.stack, v) }

func (f *frame) pop() runtime.Value {
	v := f.stack[len(f.stack)-1]
	f.stack = f.stack[:len(f.stack)-1]
	return v
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
