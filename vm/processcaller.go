package vm

import "github.com/podhmo/minigo/runtime"

// processCaller is the caller a host builtin may retain. Its execution methods
// keep the process in which it was obtained, even after the public VM is reused
// for another root Call. The other methods are execution-local: embedding the
// VM preserves the frame/type context of the host call currently running.
type processCaller struct {
	*VM
	p *proc
}

// bindCaller leaves synchronous-only and foreign callers alone. A bare VM used
// outside a process keeps its existing host-driven behavior; calls executing
// script always have a process before crossing a host boundary.
func bindCaller(vc runtime.VMCaller) runtime.VMCaller {
	if v, ok := vc.(*VM); ok {
		if p := v.proc.Load(); p != nil {
			return processCaller{VM: v, p: p}
		}
	}
	return vc
}

// anchor makes an idle execution context tied to this process, never the root
// VM's current one. Its metadata/memos can also be used by a foreign callback's
// argument/result adapters without touching the original owner's frames.
func (c processCaller) anchor() *VM {
	v := &VM{H: c.H, task: c.task}
	v.proc.Store(c.p)
	return v
}

func (c processCaller) Call(fn runtime.Value, args []runtime.Value) (runtime.Value, error) {
	return c.call(fn, args, false)
}

func (c processCaller) CallCatching(fn runtime.Value, args []runtime.Value) (runtime.Value, error) {
	return c.call(fn, args, true)
}

func (c processCaller) call(fn runtime.Value, args []runtime.Value, catch bool) (runtime.Value, error) {
	select {
	case <-c.p.done:
		return nil, procExit{}
	default:
	}
	gid := goroutineID()
	c.callMu.Lock()
	if c.proc.Load() == c.p && c.callDepth > 0 && c.callGid == gid {
		// Same-owner synchronous reentry retains its frames and recovery
		// context. An idle or reused root is never a callback entry point.
		c.callDepth++
		c.callMu.Unlock()
		return c.callEntered(fn, args, nil, nil)
	}
	c.callMu.Unlock()
	// Preserve the existing distinction between a blocking host helper's
	// joined reentry, a genuinely asynchronous panic, and FuncMap catching.
	failProc := !catch && !c.p.isSyncCaller(gid)
	t := c.anchor().spawn(fn, args, nil, nil, failProc)
	err := t.Wait()
	return t.Result, err
}

func (c processCaller) Spawn(fn runtime.Value, args []runtime.Value) *runtime.Task {
	return c.anchor().Spawn(fn, args)
}

// Host interface/error proxies probe methods before invoking them. These two
// metadata operations may therefore happen on a retained caller off-owner.
func (c processCaller) Member(value runtime.Value, name string) (runtime.Value, bool) {
	return callbackContext(c).(processCaller).VM.Member(value, name)
}

func (c processCaller) MethodSetOf(value runtime.Value) (map[string]bool, bool) {
	return callbackContext(c).(processCaller).VM.MethodSetOf(value)
}

// callbackContext is for adapters that do work outside Call itself (method
// selection, marshaling returned values). Only the active owner may use its
// execution-local metadata. Foreign and stale callbacks get private metadata
// while retaining the same process-bound Call/Spawn capability.
func callbackContext(vc runtime.VMCaller) runtime.VMCaller {
	if c, ok := vc.(processCaller); ok {
		gid := goroutineID()
		c.callMu.Lock()
		owner := c.proc.Load() == c.p && c.callDepth > 0 && c.callGid == gid
		c.callMu.Unlock()
		if !owner {
			return processCaller{VM: c.anchor(), p: c.p}
		}
	}
	return vc
}
