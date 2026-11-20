// Package process holds the minigo process: the unit a VM's spawned
// goroutines share — its done signal, first fatal error, deadlock
// detection and managed host-channel liveness. The VM interacts with a
// Proc only through spawn/park/unpark-shaped calls.
package process

import (
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// proc is one interpreter process: the goroutines belonging to a root Call.
type Proc struct {
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
	// mints is the process-wide uintptr-mint table shared by the
	// process's goroutine VMs (see UpsertUintptr).
	mints *uintMint
}

// chanLive is one managed host channel's liveness record. rv pins the
// hchan so its pointer stays unique while registered (a freed hchan's
// address could otherwise be recycled under a new, live channel).

// chanLive is one managed host channel's liveness record. rv pins the
// hchan so its pointer stays unique while registered (a freed hchan's
// address could otherwise be recycled under a new, live channel).
type chanLive struct {
	rv   reflect.Value
	dead bool // no further sends can arrive (stopped, or a one-shot fire)
}

// gwait is one live goroutine's wait state for deadlock detection.

// gwait is one live goroutine's wait state for deadlock detection.
type gwait struct {
	refs       int  // nested Calls on the same goroutine share the state
	parked     bool // currently blocked on a watched op
	wakeable   bool // the current park can end without a script goroutine (computed from wakeMisc + wakeChans)
	wakeMisc   bool // wake sources other than managed channels (host joins, unmanaged host channels)
	wakeChans  map[uintptr]struct{}
	parkedOnly bool // registered by a bare park outside any Call — no LeaveG will come, so the entry dies with its unpark
}

// deadlockError is the process fatal raised when every live goroutine is
// parked with no possible wake — the analogue of the gc runtime's
// "all goroutines are asleep" check.

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

// deadlockQuiescence is how long the all-parked state must hold before
// the process is declared deadlocked: a wake already in flight (a
// completed send whose receiver has not yet resumed) disarms the watch
// during the delay instead of racing a false fatal.
const deadlockQuiescence = 200 * time.Millisecond

// uintMint is the process-wide uintptr-mint table: `unsafe.Pointer(x)`
// on an integer payload mints an address-valued pointer, and converting
// it back to *T revives the recorded referent — a goroutine arg minted
// on the parent lands in the same table as the child's lookup. An entry
// may implement `Dead() bool` (the vm's box-gated mints): a dead entry
// has no live unsafe.Pointer box carrying the address, so inserting
// past the cap sweeps dead entries before any live one is evicted.
type uintMint struct {
	q []uint64
	m map[uint64]any
}

const uintMintCap = 4096

// mintDead is the contract a vm mint entry satisfies for reaping.
type mintDead interface {
	Dead() bool
}

// UpsertUintptr merges into addr's live entry via merge, or records
// make() when none exists — the atomic attach-or-mint a second mint of
// the same address needs so it keeps the earlier boxes' liveness
// records instead of replacing them.
func (p *Proc) UpsertUintptr(addr uint64, make_ func() any, merge func(entry any)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mints == nil {
		p.mints = &uintMint{m: map[uint64]any{}}
	}
	if old, dup := p.mints.m[addr]; dup {
		if d, dead := old.(mintDead); dead && d.Dead() {
			delete(p.mints.m, addr)
		} else {
			merge(old)
			return
		}
	}
	p.mints.q = append(p.mints.q, addr)
	p.mints.m[addr] = make_()
	if len(p.mints.q) > uintMintCap {
		// reap dead entries first: they can never revive, and a
		// long-running mint loop (a temp buffer per iteration) mostly
		// accumulates those. The compaction only keeps keys still in
		// the map — the new address must already be recorded above or
		// it would drop out of the queue and stay in the map forever.
		for a, r := range p.mints.m {
			if d, ok := r.(mintDead); ok && d.Dead() {
				delete(p.mints.m, a)
			}
		}
		q := p.mints.q[:0]
		for _, a := range p.mints.q {
			if _, ok := p.mints.m[a]; ok {
				q = append(q, a)
			}
		}
		p.mints.q = q
		for len(p.mints.q) > uintMintCap {
			delete(p.mints.m, p.mints.q[0])
			p.mints.q = p.mints.q[1:]
		}
	}
}

// AttachUintptr runs fn on the live entry for addr under the table
// lock — the vm records each minted unsafe.Pointer box against its
// entry this way. Reports whether a live entry was found.
func (p *Proc) AttachUintptr(addr uint64, fn func(entry any)) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mints == nil {
		return false
	}
	ref, ok := p.mints.m[addr]
	if !ok {
		return false
	}
	if d, isDead := ref.(mintDead); isDead && d.Dead() {
		delete(p.mints.m, addr)
		return false
	}
	fn(ref)
	return true
}

// ReviveUintptr reports the referent a mint recorded for addr — the
// *T-conversion side of the uintptr round trip. A dead entry reports
// miss and frees its key.
func (p *Proc) ReviveUintptr(addr uint64) (any, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mints == nil {
		return nil, false
	}
	ref, ok := p.mints.m[addr]
	if !ok {
		return nil, false
	}
	if d, isDead := ref.(mintDead); isDead && d.Dead() {
		delete(p.mints.m, addr)
		return nil, false
	}
	return ref, true
}

func New() *Proc { return &Proc{done: make(chan struct{})} }

// kill ends the process: parked channel operations abort with procExit.

// kill ends the process: parked channel operations abort with procExit.
func (p *Proc) Kill() { p.doneOnce.Do(func() { close(p.done) }) }

// fail records the process's first fatal error and ends the process.

// fail records the process's first fatal error and ends the process.
func (p *Proc) Fail(err error) {
	p.mu.Lock()
	if p.fatal == nil {
		p.fatal = err
	}
	p.mu.Unlock()
	p.Kill()
}

// WatchCallFrom marks gid as a synchronous re-entry source — see
// syncCallers — and returns the func that unmarks it.

// WatchCallFrom marks gid as a synchronous re-entry source — see
// syncCallers — and returns the func that unmarks it.
func (p *Proc) WatchCallFrom(gid int64) func() {
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

// IsSyncCaller reports whether gid is a helper running a blocking host
// call — a Call from it re-enters synchronously (see syncCallers).

// IsSyncCaller reports whether gid is a helper running a blocking host
// call — a Call from it re-enters synchronously (see syncCallers).
func (p *Proc) IsSyncCaller(gid int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.syncCallers[gid]
	return ok
}

func (p *Proc) FatalErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fatal
}

// EnterG registers a goroutine that can run script for this process:
// every Call's goroutine, whether the root call, a spawned task, or a
// host callback's spawned re-entry. Nested Calls on one goroutine share
// the state through refs.

// EnterG registers a goroutine that can run script for this process:
// every Call's goroutine, whether the root call, a spawned task, or a
// host callback's spawned re-entry. Nested Calls on one goroutine share
// the state through refs.
func (p *Proc) EnterG(gid int64) {
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

// LeaveG releases a goroutine registration at Call exit.

// LeaveG releases a goroutine registration at Call exit.
func (p *Proc) LeaveG(gid int64) {
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

// Wake describes how a parked op can still wake without a script
// goroutine acting: misc covers host-call joins and unmanaged host
// channels, chans the managed host channels whose liveness is tracked
// (see proc.chans — a parked receiver on a stopped timer's channel must
// count as asleep once the channel dies).

// Wake describes how a parked op can still wake without a script
// goroutine acting: misc covers host-call joins and unmanaged host
// channels, chans the managed host channels whose liveness is tracked
// (see proc.chans — a parked receiver on a stopped timer's channel must
// count as asleep once the channel dies).
type Wake struct {
	Misc  bool
	Chans []uintptr
}

// chanPtr is the single managed-channel id a channel op resolved to —
// chanOf produces at most one tracked channel per op.

// chanPtr is the single managed-channel id a channel op resolved to —
// chanOf produces at most one tracked channel per op.
func (w Wake) ChanPtr() uintptr {
	if len(w.Chans) > 0 {
		return w.Chans[0]
	}
	return 0
}

// gWakeable computes whether the parked op can end without a script
// goroutine acting: a misc source, or a managed channel still alive.
// Callers hold p.mu.

// gWakeable computes whether the parked op can end without a script
// goroutine acting: a misc source, or a managed channel still alive.
// Callers hold p.mu.
func (p *Proc) gWakeable(g *gwait) bool {
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

// Park marks a goroutine blocked on a watched op. w reports whether the
// op can end without another script goroutine acting — a host channel
// (timers, retained callback feeds) or a host call's join — in which
// case the park never counts toward deadlock.

// Park marks a goroutine blocked on a watched op. w reports whether the
// op can end without another script goroutine acting — a host channel
// (timers, retained callback feeds) or a host call's join — in which
// case the park never counts toward deadlock.
func (p *Proc) Park(gid int64, w Wake) {
	if gid == 0 {
		return
	}
	p.mu.Lock()
	g := p.gs[gid]
	if g == nil {
		// an unregistered parker still counts — it is a real goroutine.
		// Its registration lives as long as the park: no Call frame
		// exists to leave it later, so Unpark deletes the entry.
		g = &gwait{refs: 1, parkedOnly: true}
		p.gs[gid] = g
		p.live++
	}
	if !g.parked {
		g.parked = true
		g.wakeMisc = w.Misc
		if len(w.Chans) > 0 {
			g.wakeChans = make(map[uintptr]struct{}, len(w.Chans))
			for _, ptr := range w.Chans {
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

// Unpark clears a goroutine's parked mark when its op completes.

// Unpark clears a goroutine's parked mark when its op completes.
func (p *Proc) Unpark(gid int64) {
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

// touch records any wait-state change: a wake, a new goroutine, or a
// pending external callback all invalidate an armed watch.
func (p *Proc) touch() {
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

// checkDeadLocked arms the deadlock watch once every live goroutine is
// parked on a wakeless op with no pending external callback. The watch
// retires early on any state change or on process death, so it never
// outlives its proc. Callers hold p.mu.
func (p *Proc) checkDeadLocked() {
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
			p.Fail(deadlockError{})
		}
	}()
}

// NoteExternalWait records a pending external callback on this process —
// a timer or retained host hook that will run script code later. While
// one is pending the deadlock check stays disarmed: the callback is a
// wake source the wait state cannot see. The returned release must run
// when the callback fires or is cancelled.

// NoteExternalWait records a pending external callback on this process —
// a timer or retained host hook that will run script code later. While
// one is pending the deadlock check stays disarmed: the callback is a
// wake source the wait state cannot see. The returned release must run
// when the callback fires or is cancelled.
func (p *Proc) NoteExternalWait() (release func()) {
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
func (p *Proc) RegisterChan(rv reflect.Value) uintptr {
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

// ChanState reports a channel's managed-liveness for a park decision:
// managed+alive feeds a tracked wake, managed+dead feeds none. The
// managedN fast path keeps unmanaged channels (the common case) out of
// the mutex.
func (p *Proc) ChanState(ptr uintptr) (managed, alive bool) {
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

// SetChanDead flips a managed channel's liveness and re-evaluates the
// parked goroutines waiting on it: a park whose last wake source died
// joins the asleep count (and can now fatal), while a re-armed channel
// (Timer.Reset) lifts its waiters back out.

// SetChanDead flips a managed channel's liveness and re-evaluates the
// parked goroutines waiting on it: a park whose last wake source died
// joins the asleep count (and can now fatal), while a re-armed channel
// (Timer.Reset) lifts its waiters back out.
func (p *Proc) SetChanDead(ptr uintptr, dead bool) {
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

// ParkedChanSend sends on ch parked like a script channel op: the block
// counts toward deadlock detection — a rendezvous channel like
// iter.Pull's coroutine is resumed only by a script goroutine, so it
// reports no external wake source — and the op unwinds with a procExit
// panic when vc's process ends instead of parking past it.
// Done is the process-done channel: closed by Kill, parked ops select on
// it to abort with procExit.
func (p *Proc) Done() chan struct{} { return p.done }
