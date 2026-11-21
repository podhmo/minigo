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
	"reflect"
	"runtime/debug"

	"github.com/podhmo/minigo/runtime"
)

// procExit is the unwind raised in a parked goroutine when its process
// ends (root Call returned, or a sibling goroutine's panic failed the
// process). It is deliberately not a *runtime.Panic: defers still run
// but recover() cannot see it, and it is swallowed at the spawn
// boundary — the Go analogue is process exit, where other goroutines
// die without a catchable panic.
type procExit struct{}

func (procExit) Error() string { return "minigo: process exited" }

// ExitRequest is the error for a script's os.Exit(code): like a real
// process exit it skips every pending defer, cannot be recovered by the
// script, and stops the process's other goroutines. [VM.Call] returns it
// for a nonzero code and a nil error for code 0.
type ExitRequest struct{ Code int }

func (e *ExitRequest) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// IsProcExit reports whether err is the error a goroutine gets when its
// process ends under it (the root Call returned, or a sibling goroutine
// failed the process). Host code collecting [runtime.Task] errors can skip
// these to find the real failure.
func IsProcExit(err error) bool { _, ok := err.(procExit); return ok }

// Recover implements [runtime.VMCaller]: it returns the in-flight panic
// value when called directly by a deferred function, else nil.
func (v *VM) Recover() runtime.Value {
	if v.inflight == nil {
		return runtime.NIL
	}
	// Go's rule (gorecover): exactly one non-wrapper frame may sit
	// between the recover() call and the panic's unwind — recover()
	// works when the deferred function calls it directly. `defer
	// recover()` puts zero frames between (the panic's own unwind runs
	// the builtin), and a recover() inside a nested callee puts two or
	// more — both return nil. Sentinel frames are the wrappers and do
	// not count.
	dist := 0
	for i := len(v.frames) - 1; i >= v.unwindDepth && i >= 0; i-- {
		if !v.frames[i].sentinel {
			dist++
		}
	}
	if dist != 1 {
		return runtime.NIL
	}
	val := v.inflight.Value
	v.consumedPanic = v.inflight
	v.consumedPanic.Recovered = true
	v.inflight = nil
	// a still-constant payload materializes — panic's `any` parameter is
	// where `panic(4)` becomes an int, so recover() never hands back
	// the constant domain's operand (recover().(int) asserted "int, not
	// int").
	if u, ok := constPayload(val); ok {
		var ctd *runtime.TypeDef
		if n, isNamed := val.(*runtime.Named); isNamed {
			ctd = n.Typ
		}
		if mv, err := v.materializeConstErr(u, ctd); err == nil {
			val = mv
		}
	}
	// a typed-nil payload recovers as the non-nil interface value gc's
	// `recover() any` returns — the dynamic type rides along, so
	// `r != nil` reports true and `r.(*T)` still asserts the typed nil.
	if tn, ok := val.(*runtime.TypedNil); ok {
		return &runtime.IfaceNil{Typ: tn.Typ}
	}
	// a runtime-error payload surfaces as the boxed host error Go's
	// recover() returns — `err.(error)` asserts and `.Error()` calls
	// resolve through the reflection path. The same goes for the
	// plainError family and panic(nil)'s PanicNilError.
	if e, ok := val.(error); ok {
		return &runtime.GoValue{V: e}
	}
	return val
}

func asError(r any) error {
	switch e := r.(type) {
	case *runtime.Trap:
		return e
	case *runtime.Panic:
		return e
	case error:
		return e
	default:
		return fmt.Errorf("panic: %v", r)
	}
}

// asScriptPanic wraps a raw host panic in *runtime.Panic so unwinding can
// attribute it to script frames and deferred recover() can catch it —
// matching Go, where runtime errors (index out of range, nil deref, divide
// by zero) are recoverable panics. The original value is kept boxed so
// recover() hands scripts the real error, not its rendered text, and the
// host goroutine stack is captured while the panicking frames are still
// live, so a panic inside a builtin/host handler shows where it died.
// Trap and Panic pass through unchanged.
func asScriptPanic(r any) any {
	switch r.(type) {
	// procExit and ExitRequest pass through like Trap: a process-exit
	// unwind must not become a recoverable script panic.
	case nil, *runtime.Trap, *runtime.Panic, procExit, *ExitRequest:
		return r
	default:
		return &runtime.Panic{Value: &runtime.GoValue{V: r}, GoStack: string(debug.Stack())}
	}
}

// unwoundFrame is a frame an in-flight panic unwound, tagged with that
// panic so unwind can drop the entries of a panic that dies (recovered
// or superseded) without disturbing another live unwind's frames.
type unwoundFrame struct {
	f  *frame
	pn *runtime.Panic
}

// dropUnwound removes every unwound frame belonging to p — a nil p
// drops nothing.
func (v *VM) dropUnwound(p *runtime.Panic) {
	if p == nil {
		return
	}
	kept := v.unwinding[:0]
	for _, e := range v.unwinding {
		if e.pn != p {
			kept = append(kept, e)
		}
	}
	v.unwinding = kept
}

// unwind runs the frame's defers and resolves the outcome of r, the value
// caught during frame teardown (nil on normal return).
func (v *VM) unwind(f *frame, r any) {
	if r != nil {
		v.trace(f, r)
	}
	var p *runtime.Panic
	if sp, ok := r.(*runtime.Panic); ok {
		p = sp
		// a panic unwinding on top of another chains to it — gc prints
		// the superseded panic first (`panic: old` / `\tpanic: new`),
		// with ` [recovered]` when recover() had consumed it. The
		// in-flight panic is the topmost superseded one; a consumed
		// panic is what remains once recover() cleared inflight. p
		// itself stays out — this is the same panic's later unwind
		// frames.
		if p.Prev == nil {
			if v.inflight != nil && v.inflight != p {
				p.Prev = v.inflight
			} else if v.consumedPanic != nil && v.consumedPanic != p {
				p.Prev = v.consumedPanic
			}
		}
	}
	// v.inflight is visible to recover() only while this frame's defers run.
	// os.Exit skips the defers on every frame like a real process exit.
	// A frame completing normally (r == nil) owns neither: it stays on the
	// frame stack while its defers run, a real frame between their
	// recover() calls and the panic still unwinding below — the way a
	// `defer recover()` inside a deferred call catches the panic that
	// invoked it.
	saved := v.inflight
	savedD := v.unwindDepth
	// dpos is the frame's logical position while its defers drain — the
	// boundary a panic raised inside one of them unwinds to.
	dpos := len(v.frames)
	if r != nil {
		v.inflight = p
		v.unwindDepth = dpos
	} else {
		v.frames = append(v.frames, f)
		defer v.framesPop()
	}
	if _, isExit := r.(*ExitRequest); !isExit {
		// A panic consumed partway through the drain hands the rest of
		// the list to the outer context — Go's recovery lands on the
		// frame's deferreturn, where `defer recover()` sees the panic
		// that was unwinding below (recover1.go test6).
		v.draining++
		// runOneDefer can re-panic out of the loop (a deferred call's
		// trap or host panic propagates); the count must not leak.
		func() {
			defer func() { v.draining-- }()
			for len(f.defers) > 0 {
				if p != nil && v.inflight == nil {
					v.inflight = saved
					v.unwindDepth = savedD
					// the consumed panic's unwound frames die with it — Go
					// lists only live frames once an unwind is recovered.
					// Drop the frame's own panic too: it is dead whether it
					// was consumed itself or superseded mid-drain.
					v.dropUnwound(p)
					v.dropUnwound(v.consumedPanic)
					v.consumedPanic = nil
					v.frames = append(v.frames, f)
					defer v.framesPop()
					p = nil
				}
				v.runOneDefer(f, p)
			}
		}()
	}
	if r != nil {
		if p != nil || (v.inflight != saved && v.inflight != nil) {
			// the drain left a panic to propagate — either the original
			// still unwinding or one a deferred call raised. The latter
			// must be picked up even after a mid-drain recovery set p
			// to nil: a panic raised in a later deferred call would
			// otherwise be dropped (and leak into inflight for an
			// unrelated recover() to find).
			p = v.inflight
			v.inflight = saved
			v.unwindDepth = savedD
		}
		// consumed mid-drain with no new panic: the transition already
		// restored the outer panic state.
	} else if v.inflight != saved && v.inflight != nil {
		// a deferred call panicked during a normal drain — propagate it
		// as this frame's panic. A consumed outer panic leaves inflight
		// nil and dies with it. Restore the outer panic state like the
		// r!=nil path: runOneDefer installs the deferred panic on
		// inflight, and propagating it upward while inflight still holds
		// it makes every enclosing unwind read it as the *outer* panic —
		// a try-style recover then restores the consumed panic into
		// inflight, leaking it into an unrelated later recover().
		p = v.inflight
		v.inflight = saved
		v.unwindDepth = savedD
	}
	if r != nil && p == nil {
		// the panic died here (recovered, or it was a Trap swallowed
		// at a boundary) — its unwound frames die with it; entries of
		// a still-live outer unwind stay.
		if sp, ok := r.(*runtime.Panic); ok {
			v.dropUnwound(sp)
		}
		v.dropUnwound(v.consumedPanic)
		v.consumedPanic = nil
	}
	switch {
	case p != nil:
		v.failProc(p)
		panic(p) // still panicking, or a deferred call panicked
	case r != nil:
		if _, ok := r.(*runtime.Panic); !ok {
			v.failProc(r)
			panic(r) // Trap/host panic: recover() must not swallow it
		}
		fallthrough // script panic recovered by a deferred function
	default:
		// keep the stack's backing: a fresh []runtime.Value{…} returns
		// cap-1 to the frame pool and the next call reallocates.
		// finalResult reads f.results/locals — never f.stack — so
		// truncating first cannot corrupt the returned value.
		r := v.finalResult(f)
		f.setSp(0)
		f.push(r)
	}
}

// failProc ends the process when an unrecovered panic or trap reaches a
// goroutine's root frame — the instant Go exits on. Killing here, inside
// the unwind that just decided the panic is fatal, beats waiting for the
// spawn boundary's error path: a sibling a dying defer unblocked (wg.Done
// mid-unwind) is already dead when it resumes instead of printing past
// the crash. procExit and ExitRequest are excluded — the process is
// already ending on its own terms, and recording procExit as the fatal
// would mask the real failure on the root call.
func (v *VM) failProc(r any) {
	p := v.proc.Load()
	if len(v.frames) != 0 || v.draining != 0 || p == nil || v.syncCall {
		return
	}
	switch r := r.(type) {
	case *runtime.Panic:
		// the panic renders after the process dies — an Error()/String()
		// probe must not Call through this VM: its proc is the one being
		// killed, so the call would die procExit before the method runs.
		// Repoint every panic in the chain to a caller bound to no
		// process. (A panic on a live or recovered path keeps the caller
		// it was raised with.)
		caller := v.probeCaller()
		for pp := r; pp != nil; pp = pp.Prev {
			pp.VC = caller
		}
		p.Fail(r)
	case *runtime.Trap:
		p.Fail(asError(r))
	}
}

// finalResult computes the frame's return value after its defers ran, so a
// deferred function can still mutate named results (Go semantics). Named
// slots are gathered whenever the function declares named results — even on
// panic unwind, where no OpReturn ever ran (a recover()ing defer can set
// them). Unnamed results come from the values OpReturn collected; a frame
// that never reached a return — a recovered panic ended it — yields the
// declared result types' zeros (`f() int` answers 0, not nil).
func (v *VM) finalResult(f *frame) runtime.Value {
	results := f.results
	if len(f.ch.NamedSlots) > 0 {
		results = make([]runtime.Value, len(f.ch.NamedSlots))
		for i, s := range f.ch.NamedSlots {
			results[i] = f.localVal(s)
		}
	}
	if len(results) == 0 && f.ch.NResults > 0 {
		results = v.zeroResults(f)
	}
	switch len(results) {
	case 0:
		return runtime.NIL
	case 1:
		return results[0]
	default:
		return &runtime.Tuple{Elems: results}
	}
}

// zeroResults builds each declared result type's zero value — the value a
// function returns when a panic recovered by a deferred call ended it
// before its first return. Result types resolve through the frame's file
// scope like any declared-type expression; an unresolvable one keeps NIL.
func (v *VM) zeroResults(f *frame) []runtime.Value {
	fn := f.fn
	if fn == nil || fn.Decl == nil || fn.Decl.Type == nil || fn.Decl.Type.Results == nil {
		return nil
	}
	s := &specialCtx{v: v, f: f, q: &runtime.QuotedCall{File: fn.File}}
	out := make([]runtime.Value, 0, f.ch.NResults)
	for _, fld := range fn.Decl.Type.Results.List {
		n := len(fld.Names)
		if n == 0 {
			n = 1
		}
		td, err := s.ResolveType(fld.Type)
		for i := 0; i < n; i++ {
			if err != nil || td == nil {
				out = append(out, runtime.NIL)
				continue
			}
			out = append(out, v.zeroValue(f, td))
		}
	}
	return out
}

// runOneDefer invokes the innermost pending deferred call of f.
// unwinding is the panic unwinding f (nil on a normal-return drain or
// once it was recovered mid-drain).
func (v *VM) runOneDefer(f *frame, unwinding *runtime.Panic) {
	d := f.defers[len(f.defers)-1]
	f.defers = f.defers[:len(f.defers)-1]
	// depth is the slot this deferred call's frame occupies — the boundary
	// a panic it raises unwinds to. While the owner drains after a normal
	// return the owner is still on the stack (depth > dpos); while its
	// unwind drains, depth == dpos. A recover() inside a later deferred
	// call counts frames above depth — the owner below does not count.
	depth := len(v.frames)
	defer func() {
		if v.H.Prof != nil {
			// charge the deferred call (a host call has no frame of its
			// own) to the frame draining it, not to its caller.
			v.profCheck()
		}
		r := asScriptPanic(recover())
		if r == nil {
			return
		}
		// a panic raised by the deferred call has no link back to
		// the frame that registered it. While f unwinds, Go runs the
		// deferred call on top of the panicking frames, so its
		// traceback continues with the frames the unwinding panic
		// collected — the original panic site, even when the deferred
		// call recovered and re-panicked. Otherwise record the defer
		// site as a synthetic entry so the traceback shows who
		// deferred it.
		below := []string{v.frameLine(f, d.pos, v.frameName(f)+" (deferred call)")}
		if unwinding != nil && len(unwinding.Frames) > 0 && r != any(unwinding) {
			below = unwinding.Frames
		}
		switch e := r.(type) {
		case *runtime.Panic:
			e.Frames = append(e.Frames, below...)
			v.inflight = e
			v.unwindDepth = depth
		case *runtime.Trap:
			e.Frames = append(e.Frames, below...)
			panic(r)
		case procExit:
			// process teardown: a deferred call that parks again
			// exits immediately — keep draining the remaining
			// defers (Goexit drains them too)
		default:
			panic(r)
		}
	}()
	v.invokeDeferred(d)
}

// invokeDeferred runs one deferred call. Callees without a bytecode frame
// (BuiltinFunc, TypeDef conversion, Cell-wrapped values) still run at
// teardown; a sentinel frame is pushed for them so recover() counts the
// distance correctly: the sentinel marks the deferred call's own wrapper
// slot without counting as a real frame — `defer recover()` sees zero
// frames and returns nil, while `defer recover()` inside a deferred
// function sees exactly one and recovers, matching Go.
func (v *VM) invokeDeferred(d deferredCall) {
	callee := d.fn
	var seen map[*runtime.Function]bool
	for {
		if fn := linknameDecl(callee); fn != nil && v.H.Linkname != nil {
			tv, ok, err := v.H.Linkname(v, fn)
			if err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			if ok && !seen[fn] {
				if seen == nil {
					seen = map[*runtime.Function]bool{}
				}
				seen[fn] = true
				callee, d.args = relinkCallee(callee, tv, d.args)
				continue
			}
		}
		switch c := callee.(type) {
		case *runtime.BuiltinFunc:
			v.pushDeferredSentinel(c.Name)
			defer v.framesPop()
			args := d.args
			if !c.Forwards {
				args = expandSpread(args)
			}
			if c.Pkg != nil {
				// a deferred bound call sees the constants call() hands
				// it: `defer strconv.FormatUint(1<<63, 10)` is legal.
				var err error
				if args, err = v.matBuiltinArgs(args, c.Target); err != nil {
					err = fmt.Errorf("%s %w", c.Name, err)
					panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
				}
			}
			if _, err := c.Fn(bindCaller(v), args); err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			return
		case *runtime.TypeDef:
			v.pushDeferredSentinel(c.Name)
			defer v.framesPop()
			if _, err := v.convert(c, firstArg(d.args)); err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			return
		case *runtime.Named:
			callee = runtime.Unwrap(callee)
			continue
		case *runtime.GoValue:
			// a deferred host func — context.WithCancel's CancelFunc
			// boxes run the same way a direct Call does, through
			// reflect, on a sentinel frame so recover() distances hold.
			fv := reflect.ValueOf(c.V)
			if !fv.IsValid() || fv.Kind() != reflect.Func {
				break
			}
			v.pushDeferredSentinel(fv.Type().String())
			defer v.framesPop()
			if _, err := callReflectFunc(fv.Type().String(), fv, v, expandSpread(d.args)); err != nil {
				panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
			}
			return
		default:
			if dv, ok := runtime.Deref(callee); ok {
				callee = dv
				continue
			}
		}
		break
	}
	// calling a nil function value panics like a nil deref in Go — at
	// the deferred call's invocation, not when `defer` registered it.
	if callee == nil || callee == runtime.NIL {
		panic(runtime.NilDerefPanic())
	}
	if _, ok := asTypedNil(callee); ok {
		panic(runtime.NilDerefPanic())
	}
	fr, err := v.prepFrameSeen(callee, d.args, -1, d.statics, d.spreadTd, seen)
	if err != nil {
		panic(&runtime.Trap{Pos: d.pos, Reason: err.Error(), Err: err})
	}
	v.exec(fr)
	freeFrame(fr)
}

// pushDeferredSentinel records a deferred host call on the frame stack so
// Recover() measures the right distance to the panic's unwind. It is not
// a real frame: no chunk, no locals — it occupies the deferred call's
// slot without counting as a frame between.
func (v *VM) pushDeferredSentinel(name string) {
	v.frames = append(v.frames, &frame{fn: &runtime.Function{Name: name}, sentinel: true, v: v, base: len(v.vals)})
}

// gopanicCallSite is the fabricated frame Callers interposes between a
// deferred chain and its panic — where gc lists runtime.gopanic
// (runtime/panic.go in the Go toolchain that generated the oracles).
var gopanicCallSite = runtime.CallSite{
	Name: "runtime.gopanic",
	File: "runtime/panic.go",
	Line: 859,
}
