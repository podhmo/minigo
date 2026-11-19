// chanops.go hosts the channel operations that used to live in vm:
// resolving a script value to a channel, sending/receiving through a
// managed select (parked for deadlock detection), and the select-case
// helpers the OpSelWait machinery assembles. The VM-side operations —
// trap, park, process-done channel, zero values, typedef resolution and
// the unwind panic — arrive through ChanHooks, built per frame.
package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/internal/process"
	"github.com/podhmo/minigo/runtime"
)

// SelectCase is [reflect.SelectCase] under a local spelling so vm code
// can build case lists without importing reflect.
type SelectCase = reflect.SelectCase

// ChanHooks supplies the VM operations the channel ops need.
type ChanHooks struct {
	// VC is the calling VM, for marshal conversions.
	VC runtime.VMCaller
	// Trap panics a script trap carrying the frame's position.
	Trap func(format string, args ...any)
	// Park marks the goroutine blocked and returns the release.
	Park func(w process.Wake) func()
	// Done is the process-done channel (nil when there is no process —
	// a nil channel arm is never ready).
	Done func() chan struct{}
	// ZeroValue is the element typedef's zero for closed receives.
	ZeroValue func(et *runtime.TypeDef) runtime.Value
	// ElemTypedef resolves the channel's element typedef.
	ElemTypedef func(td *runtime.TypeDef) *runtime.TypeDef
	// Managed reports whether ptr is a managed (registered) channel —
	// its liveness is tracked for deadlock detection.
	Managed func(ptr uintptr) bool
	// Exit returns the value to panic with when the process dies.
	Exit func() any
}

// NilChanRV is a typed nil channel: never ready, parking ops forever.
var NilChanRV = reflect.ValueOf((chan runtime.Value)(nil))

// NilChanValue is a typed nil chan struct{} — the done-channel fallback.
var NilChanValue = reflect.ValueOf((chan struct{})(nil))

// RVOf is reflect.ValueOf spelled without the import — call sites that
// only pass the result through (SelArm.Case, Iterator.ChRV) stay
// reflect-import-free.
func RVOf(v any) reflect.Value { return reflect.ValueOf(v) }

// IsChan reports whether rv is a channel.
func IsChan(rv reflect.Value) bool { return rv.IsValid() && rv.Kind() == reflect.Chan }

// IsMap/IsSlice/IsArray/IsString are the Kind checks iterator setup and
// host containers run, spelled so callers stay reflect-import-free.
func IsMap(rv reflect.Value) bool    { return rv.IsValid() && rv.Kind() == reflect.Map }
func IsSlice(rv reflect.Value) bool  { return rv.IsValid() && rv.Kind() == reflect.Slice }
func IsArray(rv reflect.Value) bool  { return rv.IsValid() && rv.Kind() == reflect.Array }
func IsString(rv reflect.Value) bool { return rv.IsValid() && rv.Kind() == reflect.String }

// NewElem is reflect.New(rv.Type()).Elem() — a fresh settable value of
// rv's type (iterator copies a host array before ranging it).
func NewElem(rv reflect.Value) reflect.Value { return reflect.New(rv.Type()).Elem() }

// SelectCaseSend/Recv/Default build select cases for the OpSelWait arm
// list; Select runs the blocking select.
func SelectCaseSend(ch, sv reflect.Value) SelectCase {
	return SelectCase{Dir: reflect.SelectSend, Chan: ch, Send: sv}
}

func SelectCaseRecv(ch reflect.Value) SelectCase {
	return SelectCase{Dir: reflect.SelectRecv, Chan: ch}
}

func SelectCaseDefault() SelectCase {
	return SelectCase{Dir: reflect.SelectDefault}
}

func Select(cases []SelectCase) (int, reflect.Value, bool) {
	return reflect.Select(cases)
}

// ChanOf resolves a script value to the channel it denotes, plus the
// element typedef for closed-receive zero values and the op's wake
// picture for deadlock detection: an unmanaged host channel is Misc
// wake (the historic default — a parked host op may be fed by machinery
// the wait state cannot see), while a managed one contributes its id
// even when dead, so a later Reset can re-arm it.
func ChanOf(h *ChanHooks, x runtime.Value) (reflect.Value, *runtime.TypeDef, process.Wake) {
	switch c := x.(type) {
	case *runtime.Chan:
		return RVOf(c.C), h.ElemTypedef(c.Typ), process.Wake{}
	case *runtime.Cell:
		return ChanOf(h, c.Elem)
	case *runtime.Named:
		return ChanOf(h, c.V)
	case *runtime.IfaceNil:
		return ChanOf(h, &runtime.TypedNil{Typ: c.Typ})
	case *runtime.TypedNil:
		if c.Typ != nil && c.Typ.Kind == runtime.KindChan {
			// a nil channel never becomes ready — it parks the op
			// (blocking forever at the root, as Go's deadlock does)
			return NilChanRV, h.ElemTypedef(c.Typ), process.Wake{}
		}
		h.Trap("channel operation on %T", x)
	case runtime.Nil:
		// host-returned nil channels arrive untyped (GoValueOf folds nil
		// Chans/pointers to Nil for `v == nil` reads); the only nil that
		// reaches a channel op is a nil channel — park it like a typed one.
		return NilChanRV, nil, process.Wake{}
	case *runtime.GoValue:
		if rv := reflect.ValueOf(c.V); IsChan(rv) {
			return rv, nil, ChanParkWake(h, rv)
		}
		h.Trap("channel operation on non-channel host value %T", c.V)
	default:
		h.Trap("channel operation on %T", x)
	}
	return reflect.Value{}, nil, process.Wake{}
}

// ChanParkWake recomputes a host channel's wake picture at park time:
// managed channels feed a tracked wake (a dead one can still be
// re-armed), unmanaged host channels are Misc wake. Non-channel values
// and a nil proc report no wake source.
func ChanParkWake(h *ChanHooks, rv reflect.Value) process.Wake {
	if IsChan(rv) {
		if h.Managed(rv.Pointer()) {
			return process.Wake{Chans: []uintptr{rv.Pointer()}}
		}
		return process.Wake{Misc: true}
	}
	return process.Wake{}
}

// ChanSendValue marshals a value sent on a channel: `chan any` carries
// the script value verbatim so a container keeps its identity on the
// receive side (`got["k"] = 8` writes the original map); concrete
// element types marshal like a host call argument.
func (m *Marshal) ChanSendValue(vc runtime.VMCaller, val runtime.Value, elemT reflect.Type) (reflect.Value, error) {
	if elemT.Kind() == reflect.Interface && elemT.NumMethod() == 0 {
		out := reflect.New(elemT).Elem()
		if val != nil && val != runtime.NIL {
			out.Set(reflect.ValueOf(val))
		}
		return out, nil
	}
	return m.ToReflect(val, elemT, vc)
}

// ChanSend sends sv on chRV, blocking as in Go — including panicking on a
// closed channel (the host panic surfaces as a script panic).
func ChanSend(h *ChanHooks, chRV, sv reflect.Value, w process.Wake) {
	unpark := h.Park(w)
	defer unpark()
	chosen, _, _ := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: chRV, Send: sv},
		{Dir: reflect.SelectRecv, Chan: RVOf(h.Done())},
	})
	unpark()
	if chosen == 1 {
		panic(h.Exit())
	}
}

// ChanRecv receives one value from chRV, blocking as in Go:
// closed-and-empty reports (zero, false).
func ChanRecvRV(h *ChanHooks, chRV reflect.Value, et *runtime.TypeDef, w process.Wake) (runtime.Value, bool) {
	unpark := h.Park(w)
	defer unpark()
	chosen, rv, open := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: chRV},
		{Dir: reflect.SelectRecv, Chan: RVOf(h.Done())},
	})
	unpark()
	if chosen == 1 {
		panic(h.Exit())
	}
	if !open {
		if et != nil {
			return h.ZeroValue(et), false
		}
		return runtime.NIL, false
	}
	return GoValueOf(rv), true
}

// ChanZero is the OpSelWait payload for a receive arm: the received
// value, or the element type's zero when the channel was closed.
func ChanZero(h *ChanHooks, a *runtime.SelArm, open bool, rv reflect.Value) runtime.Value {
	if open {
		return GoValueOf(rv)
	}
	if a.ETyp != nil {
		return h.ZeroValue(a.ETyp)
	}
	return runtime.NIL
}
