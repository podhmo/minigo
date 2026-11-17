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
	"reflect"

	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
)

// NoteExternalWait records a pending external callback on vc's process —
// a timer or retained host hook that will run script code later. While one
// is pending, deadlock detection stays disarmed. Call release when the
// callback fires or is cancelled; it is a no-op when vc has no process.
func NoteExternalWait(vc runtime.VMCaller) (release func()) {
	if p := procOf(vc); p != nil {
		return p.NoteExternalWait()
	}
	return func() {}
}

// RegisterChan starts tracking a host channel's liveness: the channel's
// hchan ptr is the managed id for ChanState and SetChanDead. A managed
// channel counts as a wake source only while alive — once dead (a
// stopped timer, a delivered one-shot fire) a receiver parked on it
// counts as asleep, like gc's treatment of a channel no sender can
// reach.
//
// Entries deliberately live as long as the proc: a dead channel stays
// parkable, so its dead record must persist for parks that come later
// (deleting it would reclassify a new park as unmanaged-wakeable), and
// unpinning rv early would let a recycled hchan address alias this
// record. Growth is therefore bounded by the channels one proc
// registers; a script loop of time.After calls is the worst case. A
// ticker the script never Stops keeps its pump goroutine — gc leaks an
// unreferenced ticker's runtime timer the same way pre-Go-1.23.

// RegisterWakeChan starts tracking the liveness of host channel rv for
// deadlock detection: a goroutine parked on a registered channel counts as
// wakeable only while the channel is alive (see [SetWakeChanDead]). The
// registration lasts as long as vc's process. It returns the id to pass to
// [SetWakeChanDead] and [WakeChanState], or 0 when vc has no process, in
// which case the channel stays unmanaged.
func RegisterWakeChan(vc runtime.VMCaller, rv reflect.Value) uintptr {
	if p := procOf(vc); p != nil {
		return p.RegisterChan(rv)
	}
	return 0
}

// ChanState reports a channel's managed-liveness for a park decision:
// managed+alive feeds a tracked wake, managed+dead feeds none. The
// managedN fast path keeps unmanaged channels (the common case) out of
// the mutex.

// SetWakeChanDead marks a channel registered with [RegisterWakeChan] as
// dead (no sender can reach it, e.g. a stopped timer) or alive again (e.g.
// a reset timer). An unknown id is ignored.
func SetWakeChanDead(vc runtime.VMCaller, ptr uintptr, dead bool) {
	if p := procOf(vc); p != nil {
		p.SetChanDead(ptr, dead)
	}
}

// WakeChanState reports whether the channel id is registered with
// [RegisterWakeChan] and, if so, whether it is alive. (false, false) means
// the channel is unmanaged or vc has no process.

// WakeChanState reports whether the channel id is registered with
// [RegisterWakeChan] and, if so, whether it is alive. (false, false) means
// the channel is unmanaged or vc has no process.
func WakeChanState(vc runtime.VMCaller, ptr uintptr) (managed, alive bool) {
	if p := procOf(vc); p != nil {
		return p.ChanState(ptr)
	}
	return false, false
}

// EnsureProc opens a process scope on this VM, or holds the open one, so
// that goroutines spawned across several Calls share one process; without
// it, each outermost [VM.Call] ends its process on return. Pair each call
// with [VM.ReleaseProc].

// EnsureProc opens a process scope on this VM, or holds the open one, so
// that goroutines spawned across several Calls share one process; without
// it, each outermost [VM.Call] ends its process on return. Pair each call
// with [VM.ReleaseProc].
func (v *VM) EnsureProc() {
	v.callMu.Lock()
	defer v.callMu.Unlock()
	if v.proc.Load() == nil {
		v.proc.Store(process.New())
	}
	v.procHolds++
}

// ReleaseProc ends a scope opened by [VM.EnsureProc]: the process is
// killed (parked goroutines abort) when the last hold releases.

// ReleaseProc ends a scope opened by [VM.EnsureProc]: the process is
// killed (parked goroutines abort) when the last hold releases.
func (v *VM) ReleaseProc() {
	v.callMu.Lock()
	defer v.callMu.Unlock()
	v.procHolds--
	if v.procHolds <= 0 {
		if p := v.proc.Load(); p != nil {
			p.Kill()
			v.proc.Store(nil)
			v.procDead = true
		}
		v.procHolds = 0
	}
}

// doneRV is the process-done channel as a reflect select operand — nil
// (never ready) when this VM has no process.

// doneRV is the process-done channel as a reflect select operand — nil
// (never ready) when this VM has no process.
func (v *VM) doneRV() reflect.Value {
	if p := v.proc.Load(); p != nil {
		return reflect.ValueOf(p.Done())
	}
	return nilChanValue
}

var nilChanValue = reflect.ValueOf((chan struct{})(nil))

// Spawn implements [runtime.VMCaller].

// Spawn implements [runtime.VMCaller].
func (v *VM) Spawn(fn runtime.Value, args []runtime.Value) *runtime.Task {
	return v.spawn(fn, args, nil, nil, true)
}

// spawn is Spawn carrying the call site's spread element typedef into
// generic inference — `go Sum(n...)` binds the same T=int the OpCall
// path does. The public Spawn signature stays (fn, args). failProc is
// Go's crash rule: a panic in a genuinely concurrent execution fails
// the process, while a synchronous re-entry (a callback the host
// invokes inside a blocking call — sync.Once.Do's f) must instead
// propagate its panic back through the join so the caller's recover
// can run.

// spawn is Spawn carrying the call site's spread element typedef into
// generic inference — `go Sum(n...)` binds the same T=int the OpCall
// path does. The public Spawn signature stays (fn, args). failProc is
// Go's crash rule: a panic in a genuinely concurrent execution fails
// the process, while a synchronous re-entry (a callback the host
// invokes inside a blocking call — sync.Once.Do's f) must instead
// propagate its panic back through the join so the caller's recover
// can run.
func (v *VM) spawn(fn runtime.Value, args []runtime.Value, statics []*runtime.TypeDef, spreadTd *runtime.TypeDef, failProc bool) *runtime.Task {
	v.callMu.Lock()
	if v.proc.Load() == nil {
		if v.procDead {
			// the caller's process already ended — Go kills pending
			// timers with the process, so the spawn never starts.
			v.callMu.Unlock()
			t := &runtime.Task{Done: make(chan struct{}), Parent: v.task}
			t.Finish(procExit{}, true)
			return t
		}
		// a spawn outside any Call (host-driven VM) gets a detached
		// process: nothing kills it, matching a goroutine that outlives
		// the program it was expected to die with — the hold is
		// deliberately never released.
		v.proc.Store(process.New())
		v.procHolds++
	}
	p := v.proc.Load()
	select {
	case <-p.Done():
		// the process died between hold and spawn — same refusal: a
		// dead process starts no new work.
		v.callMu.Unlock()
		t := &runtime.Task{Done: make(chan struct{}), Parent: v.task}
		t.Finish(procExit{}, true)
		return t
	default:
	}
	v.callMu.Unlock()
	t := &runtime.Task{Done: make(chan struct{}), Parent: v.task}
	child := &VM{H: v.H, task: t, syncCall: !failProc}
	child.proc.Store(p)
	go func() {
		r, err := child.callBounded(fn, args, statics, spreadTd)
		t.Result = r
		t.Finish(err, IsProcExit(err))
		if err != nil && !IsProcExit(err) && failProc {
			p.Fail(err)
		}
	}()
	return t
}

// probeCaller returns a caller for a fatal panic's render probe: a VM
// sharing this one's engine hooks but bound to no process. A Call on
// it lazily opens a fresh process — the way a Call on a VM that never
// ran one does — so Panic.Error()'s Error()/String() probes keep
// working after the panic's own process is dead.

// probeCaller returns a caller for a fatal panic's render probe: a VM
// sharing this one's engine hooks but bound to no process. A Call on
// it lazily opens a fresh process — the way a Call on a VM that never
// ran one does — so Panic.Error()'s Error()/String() probes keep
// working after the panic's own process is dead.
func (v *VM) probeCaller() runtime.VMCaller {
	return &VM{H: v.H}
}

// Task implements [runtime.VMCaller]; it is nil on the root goroutine.

// Task implements [runtime.VMCaller]; it is nil on the root goroutine.
func (v *VM) Task() *runtime.Task { return v.task }

// parkingHostMethods names host methods that can park indefinitely —
// their m.Call runs on a helper goroutine selected against proc.done, so
// process death unwinds the script frame with procExit even though the
// real call keeps running (the helper leaks — the documented host-park
// limit). Matching is by method name: Wait/Lock/RLock cover the sync
// primitives; Do covers sync.Once (its argument can itself block). The
// value is the park's wakeable flag for deadlock detection: only the
// timeout variants can end on their own — Wait/Lock/RLock/Do wait on
// other script goroutines acting, so they count as asleep the way gc
// counts sync waits.

// parkingHostMethods names host methods that can park indefinitely —
// their m.Call runs on a helper goroutine selected against proc.done, so
// process death unwinds the script frame with procExit even though the
// real call keeps running (the helper leaks — the documented host-park
// limit). Matching is by method name: Wait/Lock/RLock cover the sync
// primitives; Do covers sync.Once (its argument can itself block). The
// value is the park's wakeable flag for deadlock detection: only the
// timeout variants can end on their own — Wait/Lock/RLock/Do wait on
// other script goroutines acting, so they count as asleep the way gc
// counts sync waits.
var parkingHostMethods = map[string]bool{
	"Wait": false, "WaitTimeout": true,
	"Lock": false, "RLock": false, "LockTimeout": true, "TryLockTimeout": true,
	"Do": false,
}

// procDoneOf reports the caller VM's proc-done channel; non-*VM callers
// (host-side VMCaller implementations) have no process to watch.

// procDoneOf reports the caller VM's proc-done channel; non-*VM callers
// (host-side VMCaller implementations) have no process to watch.
func procDoneOf(vc runtime.VMCaller) <-chan struct{} {
	if p := procOf(vc); p != nil {
		return p.Done()
	}
	return nil
}

// procOf returns the caller VM's current proc in one read — ReleaseProc
// can nil v.proc concurrently, so check-then-use pairs must capture the
// pointer first or they race a nil receiver/field panic.

// procOf returns the caller VM's current proc in one read — ReleaseProc
// can nil v.proc concurrently, so check-then-use pairs must capture the
// pointer first or they race a nil receiver/field panic.
func procOf(vc runtime.VMCaller) *process.Proc {
	if b, ok := vc.(currentProcBearer); ok {
		return b.currentProc()
	}
	return nil
}

// park marks this goroutine blocked for deadlock detection around one
// select and returns its release: call it when the op completes. The
// deferred form also covers panic unwinds (a send on a closed channel
// panics inside reflect.Select), and it is idempotent — an early call
// plus the defer both landing is harmless. w reports whether the op can
// end without a script goroutine acting.

// park marks this goroutine blocked for deadlock detection around one
// select and returns its release: call it when the op completes. The
// deferred form also covers panic unwinds (a send on a closed channel
// panics inside reflect.Select), and it is idempotent — an early call
// plus the defer both landing is harmless. w reports whether the op can
// end without a script goroutine acting.
func (v *VM) park(w process.Wake) func() {
	p := v.proc.Load()
	if p == nil {
		return func() {}
	}
	if v.H.Prof != nil {
		// record the run up to the op: the resync on unpark drops only
		// the time the op spent actually blocked.
		v.profCheck()
	}
	gid := v.callGid
	p.Park(gid, w)
	return func() {
		p.Unpark(gid)
		v.profResync()
	}
}

// ParkedChanSend sends on ch parked like a script channel op: the block
// counts toward deadlock detection — a rendezvous channel like
// iter.Pull's coroutine is resumed only by a script goroutine, so it
// reports no external wake source — and the op unwinds with a procExit
// panic when vc's process ends instead of parking past it.
func ParkedChanSend(vc runtime.VMCaller, ch chan struct{}) {
	unpark := vcPark(vc, process.Wake{})
	defer unpark()
	select {
	case ch <- struct{}{}:
	case <-procDoneOf(vc):
		panic(procExit{})
	}
}

// ParkedChanRecv is ParkedChanSend's receive half.

// ParkedChanRecv is ParkedChanSend's receive half.
func ParkedChanRecv(vc runtime.VMCaller, ch chan struct{}) {
	unpark := vcPark(vc, process.Wake{})
	defer unpark()
	select {
	case <-ch:
	case <-procDoneOf(vc):
		panic(procExit{})
	}
}

// vcPark is park for code holding only a runtime.VMCaller (e.g.
// callReflectFunc's blocking-host join).

// vcPark is park for code holding only a runtime.VMCaller (e.g.
// callReflectFunc's blocking-host join).
func vcPark(vc runtime.VMCaller, w process.Wake) func() {
	if p, ok := vc.(vcParker); ok {
		return p.parkCaller(w)
	}
	return func() {}
}
