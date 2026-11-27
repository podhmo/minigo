package vm

import "github.com/podhmo/minigo/runtime"

// holdDeferredTrap records the first drain-defers control trap raised
// while this frame's defers are running. inflight snapshots the panic
// already unwinding at that point: like runtime.Goexit, the stop
// supersedes that panic, but a different panic afterward means a later
// deferred call panicked and must win.
func (f *frame) holdDeferredTrap(tr *runtime.Trap, inflight *runtime.Panic) {
	if f.deferredTrap == nil {
		f.deferredTrap = tr
		f.deferredTrapInflight = inflight
	}
}

// rethrowDeferredTrap restores Goexit-like control flow after the defer
// drain. A later unrecovered panic takes precedence over the stop.
func (v *VM) rethrowDeferredTrap(f *frame) {
	if f.deferredTrap == nil {
		return
	}
	tr := f.deferredTrap
	inflightAtStop := f.deferredTrapInflight
	f.deferredTrap = nil
	f.deferredTrapInflight = nil
	if v.inflight == nil || v.inflight == inflightAtStop {
		v.failProc(tr)
		panic(tr)
	}
}
