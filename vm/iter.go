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
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
)

func (v *VM) newIterator(f *frame, coll runtime.Value) *runtime.Iterator {
	return v.itFrom(f, coll, false)
}

// iterPool recycles range iterators: a loop that runs to completion
// hands its Iterator back (OpRangeNext's exit branch); a loop left by
// break, goto or panic abandons its iterator to the GC instead. The
// local cell holding the dead iterator is never read again — OpIterInit
// overwrites it when the loop is re-entered — so handing the object to
// a later loop is invisible. 'f' (range-over-func) iterators are
// allocated from the pool but never returned: an escaped yield closure
// can reference them past the loop's end.
var iterPool = sync.Pool{New: func() any { return &runtime.Iterator{} }}

func mkIter(it runtime.Iterator) *runtime.Iterator {
	p := iterPool.Get().(*runtime.Iterator)
	*p = it
	return p
}

func freeIter(it *runtime.Iterator) {
	*it = runtime.Iterator{}
	iterPool.Put(it)
}

// itFrom builds the iterator for coll. viaPtr is set once the operand
// resolved through a storage ref: `range p` on a *[N]T iterates the live
// pointee, while `range a` — an array VALUE — iterates a snapshot copy.
func (v *VM) itFrom(f *frame, coll runtime.Value, viaPtr bool) *runtime.Iterator {
	switch c := coll.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		dv, ok := runtime.Deref(coll)
		if !ok {
			f.trap("range over %T", coll)
			return nil
		}
		return v.itFrom(f, dv, true)
	case *runtime.Named:
		return v.itFrom(f, c.V, viaPtr)
	case *runtime.Slice:
		if c.Virtual() {
			// a virtual zero-size slice iterates its logical length,
			// vending the shared element value.
			return mkIter(runtime.Iterator{Kind: 's', Limit: int(c.Len()), Zero: c.Zero})
		}
		elems := c.Elems
		if !viaPtr && isArrayTyp(c.Typ) {
			// `range a` evaluates the array once — the loop reads a
			// snapshot, so writes to a's elements stay invisible.
			elems = make([]runtime.Value, len(c.Elems))
			for i, e := range c.Elems {
				elems[i] = runtime.Copy(e)
			}
		}
		return mkIter(runtime.Iterator{Kind: 's', Elems: elems})
	case *runtime.Map:
		// Snapshot the key ORDER only — Elems carries the display keys
		// and Keys their canonical forms — while pairs resolve live in
		// iterNext. Deleting the current or a reached key must not skip
		// the next (Delete tombstones its slot, never shifts the
		// snapshot), and an entry removed before its turn is not
		// produced, like Go.
		display, canonical := c.SnapshotKeys()
		return mkIter(runtime.Iterator{Kind: 'm', M: c, Elems: display, Keys: canonical})
	case *runtime.Chan:
		return mkIter(runtime.Iterator{Kind: 'c', ChRV: hostbridge.RVOf(c.C), ETyp: v.elemTypedef(f, c.Typ)})
	case *runtime.GoValue:
		rv := hostbridge.RVOf(c.V)
		switch {
		case hostbridge.IsChan(rv):
			return mkIter(runtime.Iterator{Kind: 'c', ChRV: rv, HostChan: true})
		case hostbridge.IsMap(rv):
			// a boxed host named map (url.Values, http.Header): snapshot
			// the keys, read each value live like the script map
			// iterator — an entry deleted before its turn is skipped.
			return mkIter(runtime.Iterator{Kind: 'h', HostRV: rv, HostKeys: rv.MapKeys()})
		case hostbridge.IsSlice(rv):
			// a boxed host named slice (net.IP): the length is fixed at
			// loop entry, each element is read live.
			return mkIter(runtime.Iterator{Kind: 'h', HostRV: rv, Limit: rv.Len()})
		case hostbridge.IsArray(rv):
			// an array value ranges over a copy, as in Go.
			cp := hostbridge.NewElem(rv)
			cp.Set(rv)
			return mkIter(runtime.Iterator{Kind: 'h', HostRV: cp, Limit: cp.Len()})
		case hostbridge.IsString(rv):
			return mkIter(runtime.Iterator{Kind: 'x', String: rv.String()})
		}
		f.trap("range over %T", coll)
		return nil
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		// iter.Seq/Seq2-style producer: the whole loop runs inside the
		// first OpRangeNext via yield — see driveFuncIter.
		if b, ok := coll.(*runtime.BuiltinFunc); ok && b.Seq != nil && b.Seq.Materialized != nil {
			// a seq whose elements exist already iterates them
			// directly — 'q' yields each element as the sole value,
			// like a single-value iter.Seq.
			return mkIter(runtime.Iterator{Kind: 'q', Elems: b.Seq.Materialized})
		}
		return mkIter(runtime.Iterator{Kind: 'f', Fn: c})
	case int64:
		return mkIter(runtime.Iterator{Kind: 'i', Limit: int(c)})
	case string:
		return mkIter(runtime.Iterator{Kind: 'x', String: c})
	case *runtime.TypedNil, *runtime.IfaceNil:
		if tn, ok := coll.(*runtime.TypedNil); ok && tn.Typ != nil {
			if tn.Typ.Kind == runtime.KindChan {
				// a nil channel range blocks forever, as in Go
				return mkIter(runtime.Iterator{Kind: 'c', ChRV: hostbridge.NilChanRV, ETyp: v.elemTypedef(f, tn.Typ)})
			}
			// `for i := range p` over a nil *[N]T yields 0..N-1 — only the
			// element read would dereference the nil pointer.
			if at := runtime.PtrArrayType(tn.Typ); at != nil {
				if n, ok := v.arrayLen(f, &runtime.TypeDef{Anon: at, Pkg: tn.Typ.Pkg, File: tn.Typ.File}); ok {
					return mkIter(runtime.Iterator{Kind: 'i', Limit: int(n), NilArr: true})
				}
			}
		}
		// range over a nil slice/map iterates zero times
		return mkIter(runtime.Iterator{Kind: 's'})
	case nil, runtime.Nil:
		return mkIter(runtime.Iterator{Kind: 's'})
	default:
		f.trap("range over %T", coll)
		return nil
	}
}

// iterNext pushes nvars values (key/index, elem) and returns false when done.
// elemRead reports whether a non-blank element var binds the iteration
// value — a nil *[N]T iterator may yield indices without it, but a real
// element binding dereferences the nil pointer like Go does.
func (v *VM) iterNext(f *frame, it *runtime.Iterator, nvars int, elemRead bool) bool {
	push := func(key, val runtime.Value) {
		if nvars == 2 {
			f.push(key)
			f.push(val)
			return
		}
		if nvars == 1 {
			f.push(key) // single-var range yields index/key
		}
	}
	switch it.Kind {
	case 's':
		if it.Zero != nil {
			// a virtual zero-size-element slice: Limit counts the
			// elements and each pair vends the shared value.
			if it.Idx >= it.Limit {
				return false
			}
			push(int64(it.Idx), it.Zero)
			it.Idx++
			return true
		}
		if it.Idx >= len(it.Elems) {
			return false
		}
		push(int64(it.Idx), it.Elems[it.Idx])
		it.Idx++
		return true
	case 'q':
		// a materialized single-value seq: the element is the only
		// yielded value — `for x := range seq` binds x to it. A two-var
		if it.Idx >= len(it.Elems) {
			return false
		}
		// spelling rejects the short yield like driveFuncIter's
		// missing-value detection — only once a value exists.
		if nvars == 2 {
			f.trap("yield must be called with %d argument(s), got %d", nvars, 1)
			return false
		}
		push(it.Elems[it.Idx], it.Elems[it.Idx])
		it.Idx++
		return true
	case 'm':
		for it.Idx < len(it.Keys) {
			ck, key := it.Keys[it.Idx], it.Elems[it.Idx]
			it.Idx++
			val, ok := it.M.LookupCanonical(ck)
			if !ok {
				continue // removed before being reached: not produced
			}
			push(key, val)
			return true
		}
		return false
	case 'h':
		if hostbridge.IsMap(it.HostRV) {
			for it.Idx < len(it.HostKeys) {
				k := it.HostKeys[it.Idx]
				it.Idx++
				val := it.HostRV.MapIndex(k)
				if !val.IsValid() {
					continue // removed before being reached: not produced
				}
				push(hostbridge.GoValueOf(k), hostbridge.GoValueOf(val))
				return true
			}
			return false
		}
		if it.Idx >= it.Limit {
			return false
		}
		push(int64(it.Idx), hostbridge.GoValueOf(it.HostRV.Index(it.Idx)))
		it.Idx++
		return true
	case 'i':
		if it.Idx >= it.Limit {
			return false
		}
		if it.NilArr && nvars == 2 && elemRead {
			// `for i, v := range p` on a nil *[N]T reads p[i] — a nil
			// pointer dereference on the first iteration. `for i, _ :=`
			// binds nothing and iterates the static indices like Go.
			panic(runtime.NilDerefPanic())
		}
		push(int64(it.Idx), int64(it.Idx))
		it.Idx++
		return true
	case 'x':
		if it.Idx >= len(it.String) {
			return false
		}
		r, size := utf8.DecodeRuneInString(it.String[it.Idx:])
		push(int64(it.Idx), int64(r))
		it.Idx += size
		return true
	case 'c':
		// range over a channel receives until the channel closes, blocking
		// on each element as in Go — and like Go it allows at most one
		// iteration variable (there is no index/key).
		if nvars == 2 {
			f.trap("range over channel allows at most one iteration variable")
		}
		w := process.Wake{}
		if it.HostChan {
			// a host channel is evaluated per receive: a feed that died
			// between iterations (a stopped timer) no longer wakes.
			w = hostbridge.ChanParkWake(v.chanHooks(f), it.ChRV)
		}
		val, ok := hostbridge.ChanRecvRV(v.chanHooks(f), it.ChRV, it.ETyp, w)
		if !ok {
			return false
		}
		push(val, val)
		return true
	}
	return false
}

// driveFuncIter runs a range-over-func loop (iter.Seq/Seq2 semantics) to
// completion: the producer function is invoked once with a yield builtin,
// and each yield call pushes its arguments as the loop values and re-runs
// the loop body inside the same frame — the push model of Go 1.23
// iterators on top of the pull-model VM, no coroutine needed.
//
// yield reports the body's exit back to the producer: it returns true
// when the body reached the loop's back-edge (next iteration), false when
// the body left the loop (break/goto/return), and panics when called
// again after that — matching the runtime panic for an iterator that
// continues past a false yield. A panic inside the body propagates
// through the producer's frames (its defers run, recover() may catch it)
// and then through the caller frame, as in Go.
func (v *VM) driveFuncIter(f *frame, it *runtime.Iterator, nvars, top, end int) {
	if it.Started {
		// The loop head was re-entered (e.g. a goto back into it); the
		// producer already ran to completion, so the loop is over.
		return
	}
	it.Started = true
	bodyStart := top + 1
	stackMark := f.sp
	// loopDone marks the whole loop as over once the producer returns
	// (or panics out). An escaped yield called after that must not touch
	// f: the frame may already be recycled for an unrelated call. After
	// a normal exit go panics; after a panic exit gc re-runs the body
	// closure, which a frame-based loop cannot do — trap instead.
	loopDone, returned := false, false
	defer func() {
		loopDone = true
		// A panic recovered inside the producer can leave the body's
		// operand pushes behind — restore the pre-loop depth.
		f.setSp(stackMark)
	}()
	yield := &runtime.BuiltinFunc{
		Name: "yield",
		// an intrinsic seq may call Fn straight from Go: it only pushes
		// the args and runs the body on this frame.
		DirectYield: true,
		Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if loopDone {
				if !returned {
					return nil, fmt.Errorf("unsupported: yield called after its range-over-func loop exited by panic")
				}
				panic(runtime.RuntimePanic("range function continued iteration after whole loop exit"))
			}
			if it.Exited {
				panic(runtime.RuntimePanic("range function continued iteration after function for loop body returned false"))
			}
			if nvars > 0 && len(args) < nvars {
				return nil, fmt.Errorf("yield must be called with %d argument(s), got %d", nvars, len(args))
			}
			// a producer may yield more values than the loop binds —
			// `for m := range seq2` keeps only the first, like Go.
			// Park the suspended tail first: the frames waiting on this
			// yield (the producer and its callers) own the arena region
			// right above f's window, and the body's pushes into f's
			// stack would overwrite their operands. Copy the region
			// out, run the body on a free tail, then re-append it and
			// shift the suspended bases by how much f's window grew.
			savedTop := f.base + f.sp
			parked := slices.Clone(v.vals[savedTop:])
			fIdx := -1
			for i, fr := range v.frames {
				if fr == f {
					fIdx = i
					break
				}
			}
			// susp is exactly the frames this park detaches — a frame
			// already tailParked by an enclosing yield keeps its own
			// copy and its own shift, so nested producers never
			// double-shift each other's windows.
			var susp []*frame
			if fIdx >= 0 {
				for _, fr := range v.frames[fIdx+1:] {
					if fr.base >= savedTop && !fr.tailParked {
						fr.tailParked = true
						susp = append(susp, fr)
					}
				}
			}
			v.vals = v.vals[:savedTop]
			defer func() {
				shift := len(v.vals) - savedTop
				v.vals = append(v.vals, parked...)
				for _, fr := range susp {
					fr.base += shift
					fr.tailParked = false
					fr.refresh()
				}
			}()
			for i := 0; i < nvars && i < len(args); i++ {
				f.push(args[i])
			}
			completed := false
			defer func() {
				if !completed {
					it.Exited = true // body died on panic — loop is over
				}
			}()
			f.ip = bodyStart
			v.runBounded(f, top, end)
			completed = true
			if f.ip == top {
				return true, nil // back-edge reached: next iteration
			}
			it.Exited = true
			return false, nil // body left the loop: producer must stop
		},
	}
	if _, err := v.call(it.Fn, []runtime.Value{yield}, nil, nil); err != nil {
		panic(&runtime.Trap{Pos: f.pos(), Reason: err.Error(), Err: err})
	}
	returned = true
}
