package vm

import "context"

// StopProcOnContext kills this VM's process when ctx ends. The returned
// stop function unregisters the callback when the call finishes first.
func (v *VM) StopProcOnContext(ctx context.Context) func() bool {
	if ctx == nil || ctx.Done() == nil {
		return func() bool { return false }
	}
	return context.AfterFunc(ctx, func() {
		if p := v.proc.Load(); p != nil {
			p.Kill()
		}
	})
}

// backEdge is the loop-back safepoint: profiling samples here, and a
// killed process unwinds even when a tight loop makes no calls.
func (v *VM) backEdge() {
	if v.H.Prof != nil {
		v.profCheck()
	}
	if p := v.proc.Load(); p != nil {
		select {
		case <-p.Done():
			panic(procExit{})
		default:
		}
	}
}
