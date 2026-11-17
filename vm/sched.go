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
	"sync"
	"sync/atomic"
	"time"

	"github.com/podhmo/minigo/runtime"
)

// proc is one interpreter process: the goroutines belonging to a root Call.
type proc struct {
	done     chan struct{}
	doneOnce sync.Once
	mu       sync.Mutex
	fatal    error // first goroutine failure (panic/trap/builtin error)
	// syncCallers are the helper goroutines callReflectFunc runs a
	// blocking host call on. A Call arriving from one is a synchronous
	// re-entry — the host invoking a script callback inside the call
	// (sync.Once.Do's f) — so its panic propagates back through the join
	// like any nested call's; only a genuinely foreign callback
	// (WaitGroup.Go, time.AfterFunc) fails the process the way a Go
	// goroutine's panic crashes the program.
	syncCallers map[int64]struct{}
	// Deadlock detection: goroutines that enter a Call register their id
	// in gs; channel ops and process joins mark their park. Once every
	// live goroutine is parked on an op that cannot wake without a script
	// goroutine's action — and no external callback is pending — the
	// process fails with Go's deadlock fatal, after a quiescence delay
	// that lets an in-flight wake disarm it.
	gs       map[int64]*gwait
	live     int           // registered goroutines (== len(gs), cached)
	parked   int           // live goroutines currently marked parked
	wakeable int           // parked goroutines whose op can end on its own
	extWait  int           // pending external callbacks (e.g. a scheduled time.AfterFunc)
	armed    bool          // a quiescence watch is in flight
	disarmCh chan struct{} // closed to retire the armed watch early
	epoch    uint64        // bumped on every wait-state change; the watch fires only if untouched
	// Managed host channels (timer/ticker proxy feeds) register their
	// hchan ptr here so a parked receiver's wakeable flag can follow the
	// channel's liveness: an armed feed wakes the park on its own, a dead
	// one (stopped, or a one-shot fire already delivered) does not.
	// managedN is the fast-path check for chanOf — most scripts never
	// register a channel.
	chans    map[uintptr]*chanLive
	managedN atomic.Int32
}

// chanLive is one managed host channel's liveness record. rv pins the
// hchan so its pointer stays unique while registered (a freed hchan's
// address could otherwise be recycled under a new, live channel).
type chanLive struct {
	rv   reflect.Value
	dead bool // no further sends can arrive (stopped, or a one-shot fire)
}

// gwait is one live goroutine's wait state for deadlock detection.
type gwait struct {
	refs       int  // nested Calls on the same goroutine share the state
	parked     bool // currently blocked on a watched op
	wakeable   bool // the current park can end without a script goroutine (computed from wakeMisc + wakeChans)
	wakeMisc   bool // wake sources other than managed channels (host joins, unmanaged host channels)
	wakeChans  map[uintptr]struct{}
	parkedOnly bool // registered by a bare park outside any Call — no leaveG will come, so the entry dies with its unpark
}

// deadlockError is the process fatal raised when every live goroutine is
// parked with no possible wake — the analogue of the gc runtime's
// "all goroutines are asleep" check.
type deadlockError struct{}

func (deadlockError) Error() string {
	return "fatal error: all goroutines are asleep - deadlock!"
}

// deadlockQuiescence is how long the all-parked state must hold before
// the process is declared deadlocked: a wake already in flight (a
// completed send whose receiver has not yet resumed) disarms the watch
// during the delay instead of racing a false fatal.
const deadlockQuiescence = 200 * time.Millisecond

func newProc() *proc { return &proc{done: make(chan struct{})} }

// kill ends the process: parked channel operations abort with procExit.
func (p *proc) kill() { p.doneOnce.Do(func() { close(p.done) }) }

// fail records the process's first fatal error and ends the process.
func (p *proc) fail(err error) {
	p.mu.Lock()
	if p.fatal == nil {
		p.fatal = err
	}
	p.mu.Unlock()
	p.kill()
}

// watchCallFrom marks gid as a synchronous re-entry source — see
// syncCallers — and returns the func that unmarks it.
func (p *proc) watchCallFrom(gid int64) func() {
	p.mu.Lock()
	if p.syncCallers == nil {
		p.syncCallers = map[int64]struct{}{}
	}
	p.syncCallers[gid] = struct{}{}
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.syncCallers, gid)
		p.mu.Unlock()
	}
}

// isSyncCaller reports whether gid is a helper running a blocking host
// call — a Call from it re-enters synchronously (see syncCallers).
func (p *proc) isSyncCaller(gid int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.syncCallers[gid]
	return ok
}

func (p *proc) fatalErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fatal
}

// enterG registers a goroutine that can run script for this process:
// every Call's goroutine, whether the root call, a spawned task, or a
// host callback's spawned re-entry. Nested Calls on one goroutine share
// the state through refs.
func (p *proc) enterG(gid int64) {
	if gid == 0 {
		return
	}
	p.mu.Lock()
	if p.gs == nil {
		p.gs = map[int64]*gwait{}
	}
	g := p.gs[gid]
	if g == nil {
		g = &gwait{}
		p.gs[gid] = g
		p.live++
	}
	g.refs++
	p.touch()
	p.mu.Unlock()
}

// leaveG releases a goroutine registration at Call exit.
func (p *proc) leaveG(gid int64) {
	if gid == 0 {
		return
	}
	p.mu.Lock()
	if g := p.gs[gid]; g != nil {
		g.refs--
		if g.refs <= 0 {
			if g.parked {
				p.parked--
				if g.wakeable {
					p.wakeable--
				}
			}
			delete(p.gs, gid)
			p.live--
		}
	}
	p.touch()
	// the last running goroutine leaving can itself complete the
	// deadlock state: whoever is left is all parked.
	p.checkDeadLocked()
	p.mu.Unlock()
}

// parkWake describes how a parked op can still wake without a script
// goroutine acting: misc covers host-call joins and unmanaged host
// channels, chans the managed host channels whose liveness is tracked
// (see proc.chans — a parked receiver on a stopped timer's channel must
// count as asleep once the channel dies).
type parkWake struct {
	misc  bool
	chans []uintptr
}

// chanPtr is the single managed-channel id a channel op resolved to —
// chanOf produces at most one tracked channel per op.
func (w parkWake) chanPtr() uintptr {
	if len(w.chans) > 0 {
		return w.chans[0]
	}
	return 0
}

// gWakeable computes whether the parked op can end without a script
// goroutine acting: a misc source, or a managed channel still alive.
// Callers hold p.mu.
func (p *proc) gWakeable(g *gwait) bool {
	if g.wakeMisc {
		return true
	}
	for ptr := range g.wakeChans {
		if l := p.chans[ptr]; l != nil && !l.dead {
			return true
		}
	}
	return false
}

// parkG marks a goroutine blocked on a watched op. w reports whether the
// op can end without another script goroutine acting — a host channel
// (timers, retained callback feeds) or a host call's join — in which
// case the park never counts toward deadlock.
func (p *proc) parkG(gid int64, w parkWake) {
	if gid == 0 {
		return
	}
	p.mu.Lock()
	g := p.gs[gid]
	if g == nil {
		// an unregistered parker still counts — it is a real goroutine.
		// Its registration lives as long as the park: no Call frame
		// exists to leave it later, so unparkG deletes the entry.
		g = &gwait{refs: 1, parkedOnly: true}
		p.gs[gid] = g
		p.live++
	}
	if !g.parked {
		g.parked = true
		g.wakeMisc = w.misc
		if len(w.chans) > 0 {
			g.wakeChans = make(map[uintptr]struct{}, len(w.chans))
			for _, ptr := range w.chans {
				g.wakeChans[ptr] = struct{}{}
			}
		}
		g.wakeable = p.gWakeable(g)
		p.parked++
		if g.wakeable {
			p.wakeable++
		}
	}
	p.checkDeadLocked()
	p.mu.Unlock()
}

// unparkG clears a goroutine's parked mark when its op completes.
func (p *proc) unparkG(gid int64) {
	if gid == 0 {
		return
	}
	p.mu.Lock()
	if g := p.gs[gid]; g != nil && g.parked {
		wakeable := g.wakeable
		g.parked = false
		g.wakeable = false
		g.wakeMisc = false
		g.wakeChans = nil
		p.parked--
		if wakeable {
			p.wakeable--
		}
		if g.parkedOnly {
			// a park-registered goroutine has no Call to leave — its
			// entry ends with the park instead of lingering as a live
			// ghost that never parks again.
			delete(p.gs, gid)
			p.live--
		}
	}
	p.touch()
	p.mu.Unlock()
}

// touch records any wait-state change: a wake, a new goroutine, or a
// pending external callback all invalidate an armed watch.
func (p *proc) touch() {
	p.epoch++
	if p.armed {
		p.armed = false
		close(p.disarmCh)
		p.disarmCh = nil
	}
}

// checkDeadLocked arms the deadlock watch once every live goroutine is
// parked on a wakeless op with no pending external callback. The watch
// retires early on any state change or on process death, so it never
// outlives its proc. Callers hold p.mu.
func (p *proc) checkDeadLocked() {
	if p.armed || p.live == 0 || p.parked != p.live || p.wakeable != 0 || p.extWait != 0 {
		return
	}
	p.armed = true
	epoch := p.epoch
	disarm := make(chan struct{})
	p.disarmCh = disarm
	go func() {
		t := time.NewTimer(deadlockQuiescence)
		defer t.Stop()
		select {
		case <-disarm:
			return
		case <-p.done:
			return
		case <-t.C:
		}
		p.mu.Lock()
		still := p.armed && p.epoch == epoch && p.live > 0 && p.parked == p.live && p.wakeable == 0 && p.extWait == 0
		p.mu.Unlock()
		if still {
			p.fail(deadlockError{})
		}
	}()
}

// noteExternalWait records a pending external callback on this process —
// a timer or retained host hook that will run script code later. While
// one is pending the deadlock check stays disarmed: the callback is a
// wake source the wait state cannot see. The returned release must run
// when the callback fires or is cancelled.
func (p *proc) noteExternalWait() (release func()) {
	p.mu.Lock()
	p.extWait++
	p.touch()
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.extWait--
			// the released wait may have been the only wake source
			p.checkDeadLocked()
			p.mu.Unlock()
		})
	}
}

// NoteExternalWait records a pending external callback on vc's process —
// a timer or retained host hook that will run script code later. While one
// is pending, deadlock detection stays disarmed. Call release when the
// callback fires or is cancelled; it is a no-op when vc has no process.
func NoteExternalWait(vc runtime.VMCaller) (release func()) {
	if p := procOf(vc); p != nil {
		return p.noteExternalWait()
	}
	return func() {}
}

// registerChan starts tracking a host channel's liveness: the channel's
// hchan ptr is the managed id for chanState and setChanDead. A managed
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
func (p *proc) registerChan(rv reflect.Value) uintptr {
	ptr := rv.Pointer()
	p.mu.Lock()
	if p.chans == nil {
		p.chans = map[uintptr]*chanLive{}
	}
	p.chans[ptr] = &chanLive{rv: rv}
	p.managedN.Add(1)
	p.mu.Unlock()
	return ptr
}

// RegisterWakeChan starts tracking the liveness of host channel rv for
// deadlock detection: a goroutine parked on a registered channel counts as
// wakeable only while the channel is alive (see [SetWakeChanDead]). The
// registration lasts as long as vc's process. It returns the id to pass to
// [SetWakeChanDead] and [WakeChanState], or 0 when vc has no process, in
// which case the channel stays unmanaged.
func RegisterWakeChan(vc runtime.VMCaller, rv reflect.Value) uintptr {
	if p := procOf(vc); p != nil {
		return p.registerChan(rv)
	}
	return 0
}

// chanState reports a channel's managed-liveness for a park decision:
// managed+alive feeds a tracked wake, managed+dead feeds none. The
// managedN fast path keeps unmanaged channels (the common case) out of
// the mutex.
func (p *proc) chanState(ptr uintptr) (managed, alive bool) {
	if p.managedN.Load() == 0 {
		return false, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.chans[ptr]
	if l == nil {
		return false, false
	}
	return true, !l.dead
}

// setChanDead flips a managed channel's liveness and re-evaluates the
// parked goroutines waiting on it: a park whose last wake source died
// joins the asleep count (and can now fatal), while a re-armed channel
// (Timer.Reset) lifts its waiters back out.
func (p *proc) setChanDead(ptr uintptr, dead bool) {
	p.mu.Lock()
	l := p.chans[ptr]
	if l == nil || l.dead == dead {
		p.mu.Unlock()
		return
	}
	l.dead = dead
	for _, g := range p.gs {
		if !g.parked {
			continue
		}
		if _, ok := g.wakeChans[ptr]; !ok {
			continue
		}
		if w := p.gWakeable(g); w != g.wakeable {
			g.wakeable = w
			if w {
				p.wakeable++
			} else {
				p.wakeable--
			}
		}
	}
	p.touch()
	// the flip itself can complete the deadlock state: a receiver
	// parked on the now-dead channel may have been the last wake.
	p.checkDeadLocked()
	p.mu.Unlock()
}

// SetWakeChanDead marks a channel registered with [RegisterWakeChan] as
// dead (no sender can reach it, e.g. a stopped timer) or alive again (e.g.
// a reset timer). An unknown id is ignored.
func SetWakeChanDead(vc runtime.VMCaller, ptr uintptr, dead bool) {
	if p := procOf(vc); p != nil {
		p.setChanDead(ptr, dead)
	}
}

// WakeChanState reports whether the channel id is registered with
// [RegisterWakeChan] and, if so, whether it is alive. (false, false) means
// the channel is unmanaged or vc has no process.
func WakeChanState(vc runtime.VMCaller, ptr uintptr) (managed, alive bool) {
	if p := procOf(vc); p != nil {
		return p.chanState(ptr)
	}
	return false, false
}

// EnsureProc opens a process scope on this VM, or holds the open one, so
// that goroutines spawned across several Calls share one process; without
// it, each outermost [VM.Call] ends its process on return. Pair each call
// with [VM.ReleaseProc].
func (v *VM) EnsureProc() {
	v.callMu.Lock()
	defer v.callMu.Unlock()
	if v.proc.Load() == nil {
		v.proc.Store(newProc())
	}
	v.procHolds++
}

// ReleaseProc ends a scope opened by [VM.EnsureProc]: the process is
// killed (parked goroutines abort) when the last hold releases.
func (v *VM) ReleaseProc() {
	v.callMu.Lock()
	defer v.callMu.Unlock()
	v.procHolds--
	if v.procHolds <= 0 {
		if p := v.proc.Load(); p != nil {
			p.kill()
			v.proc.Store(nil)
			v.procDead = true
		}
		v.procHolds = 0
	}
}

// doneRV is the process-done channel as a reflect select operand — nil
// (never ready) when this VM has no process.
func (v *VM) doneRV() reflect.Value {
	if p := v.proc.Load(); p != nil {
		return reflect.ValueOf(p.done)
	}
	return nilChanValue
}

var nilChanValue = reflect.ValueOf((chan struct{})(nil))

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
		v.proc.Store(newProc())
		v.procHolds++
	}
	p := v.proc.Load()
	select {
	case <-p.done:
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
			p.fail(err)
		}
	}()
	return t
}

// probeCaller returns a caller for a fatal panic's render probe: a VM
// sharing this one's engine hooks but bound to no process. A Call on
// it lazily opens a fresh process — the way a Call on a VM that never
// ran one does — so Panic.Error()'s Error()/String() probes keep
// working after the panic's own process is dead.
func (v *VM) probeCaller() runtime.VMCaller {
	return &VM{H: v.H}
}

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
var parkingHostMethods = map[string]bool{
	"Wait": false, "WaitTimeout": true,
	"Lock": false, "RLock": false, "LockTimeout": true, "TryLockTimeout": true,
	"Do": false,
}

// procDoneOf reports the caller VM's proc-done channel; non-*VM callers
// (host-side VMCaller implementations) have no process to watch.
func procDoneOf(vc runtime.VMCaller) <-chan struct{} {
	if p := procOf(vc); p != nil {
		return p.done
	}
	return nil
}

// procOf returns the caller VM's current proc in one read — ReleaseProc
// can nil v.proc concurrently, so check-then-use pairs must capture the
// pointer first or they race a nil receiver/field panic.
func procOf(vc runtime.VMCaller) *proc {
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
func (v *VM) park(w parkWake) func() {
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
	p.parkG(gid, w)
	return func() {
		p.unparkG(gid)
		v.profResync()
	}
}

// ParkedChanSend sends on ch parked like a script channel op: the block
// counts toward deadlock detection — a rendezvous channel like
// iter.Pull's coroutine is resumed only by a script goroutine, so it
// reports no external wake source — and the op unwinds with a procExit
// panic when vc's process ends instead of parking past it.
func ParkedChanSend(vc runtime.VMCaller, ch chan struct{}) {
	unpark := vcPark(vc, parkWake{})
	defer unpark()
	select {
	case ch <- struct{}{}:
	case <-procDoneOf(vc):
		panic(procExit{})
	}
}

// ParkedChanRecv is ParkedChanSend's receive half.
func ParkedChanRecv(vc runtime.VMCaller, ch chan struct{}) {
	unpark := vcPark(vc, parkWake{})
	defer unpark()
	select {
	case <-ch:
	case <-procDoneOf(vc):
		panic(procExit{})
	}
}

// vcPark is park for code holding only a runtime.VMCaller (e.g.
// callReflectFunc's blocking-host join).
func vcPark(vc runtime.VMCaller, w parkWake) func() {
	if p, ok := vc.(vcParker); ok {
		return p.parkCaller(w)
	}
	return func() {}
}
