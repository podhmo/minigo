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

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
)

var nilChanRV = reflect.ValueOf((chan runtime.Value)(nil))

// chanOf resolves a script value to the channel it denotes, plus the
// element typedef for closed-receive zero values and the op's wake
// picture for deadlock detection: an unmanaged host channel is Misc
// wake (the historic default — a parked host op may be fed by machinery
// the wait state cannot see), while a managed one contributes its id
// even when dead, so a later Reset can re-arm it.
func (v *VM) chanOf(f *frame, x runtime.Value) (reflect.Value, *runtime.TypeDef, process.Wake) {
	switch c := x.(type) {
	case *runtime.Chan:
		return reflect.ValueOf(c.C), v.elemTypedef(f, c.Typ), process.Wake{}
	case *runtime.Cell:
		return v.chanOf(f, c.Elem)
	case *runtime.Named:
		return v.chanOf(f, c.V)
	case *runtime.IfaceNil:
		return v.chanOf(f, &runtime.TypedNil{Typ: c.Typ})
	case *runtime.TypedNil:
		if c.Typ != nil && c.Typ.Kind == runtime.KindChan {
			// a nil channel never becomes ready — it parks the op
			// (blocking forever at the root, as Go's deadlock does)
			return nilChanRV, v.elemTypedef(f, c.Typ), process.Wake{}
		}
		f.trap("channel operation on %T", x)
	case runtime.Nil:
		// host-returned nil channels arrive untyped (hostbridge.GoValueOf folds nil
		// Chans/pointers to Nil for `v == nil` reads); the only nil that
		// reaches a channel op is a nil channel — park it like a typed one.
		return nilChanRV, nil, process.Wake{}
	case *runtime.GoValue:
		if rv := reflect.ValueOf(c.V); rv.IsValid() && rv.Kind() == reflect.Chan {
			return rv, nil, v.chanParkWake(rv)
		}
		f.trap("channel operation on non-channel host value %T", c.V)
	default:
		f.trap("channel operation on %T", x)
	}
	return reflect.Value{}, nil, process.Wake{}
}

// chanParkWake recomputes a host channel's wake picture at park time:
// managed channels feed a tracked wake (a dead one can still be
// re-armed), unmanaged host channels are Misc wake. Non-channel values
// and a nil proc report no wake source.
func (v *VM) chanParkWake(rv reflect.Value) process.Wake {
	if rv.IsValid() && rv.Kind() == reflect.Chan {
		if p := v.proc.Load(); p != nil {
			if managed, _ := p.ChanState(rv.Pointer()); managed {
				return process.Wake{Chans: []uintptr{rv.Pointer()}}
			}
		}
		return process.Wake{Misc: true}
	}
	return process.Wake{}
}

// chanSendValue marshals a value sent on a channel: `chan any` carries
// the script value verbatim so a container keeps its identity on the
// receive side (`got["k"] = 8` writes the original map); concrete
// element types marshal like a host call argument.
func (v *VM) chanSendValue(val runtime.Value, elemT reflect.Type) (reflect.Value, error) {
	if elemT.Kind() == reflect.Interface && elemT.NumMethod() == 0 {
		out := reflect.New(elemT).Elem()
		if val != nil && val != runtime.NIL {
			out.Set(reflect.ValueOf(val))
		}
		return out, nil
	}
	return toReflectValue(val, elemT, v)
}

// chanSend sends sv on chRV, blocking as in Go — including panicking on a
// closed channel (the host panic surfaces as a script panic).
func (v *VM) chanSend(chRV, sv reflect.Value, w process.Wake) {
	unpark := v.park(w)
	defer unpark()
	chosen, _, _ := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: chRV, Send: sv},
		{Dir: reflect.SelectRecv, Chan: v.doneRV()},
	})
	unpark()
	if chosen == 1 {
		panic(procExit{})
	}
}

// chanRecv receives one value from the channel denoted by chv, blocking
// as in Go: closed-and-empty reports (zero, false).
func (v *VM) chanRecv(f *frame, chv runtime.Value) (runtime.Value, bool) {
	chRV, et, w := v.chanOf(f, chv)
	return v.chanRecvRV(f, chRV, et, w)
}

// chanRecvRV is chanRecv on an already-resolved reflect channel — shared
// by OpRecv/OpRecvOK and channel-range iterators.
func (v *VM) chanRecvRV(f *frame, chRV reflect.Value, et *runtime.TypeDef, w process.Wake) (runtime.Value, bool) {
	unpark := v.park(w)
	defer unpark()
	chosen, rv, open := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: chRV},
		{Dir: reflect.SelectRecv, Chan: v.doneRV()},
	})
	unpark()
	if chosen == 1 {
		panic(procExit{})
	}
	if !open {
		if et != nil {
			return v.zeroValue(f, et), false
		}
		return runtime.NIL, false
	}
	return hostbridge.GoValueOf(rv), true
}

// chanZero is the OpSelWait payload for a receive arm: the received value,
// or the element type's zero when the channel was closed.
func (v *VM) chanZero(f *frame, a *runtime.SelArm, open bool, rv reflect.Value) runtime.Value {
	if open {
		return hostbridge.GoValueOf(rv)
	}
	if a.ETyp != nil {
		return v.zeroValue(f, a.ETyp)
	}
	return runtime.NIL
}
