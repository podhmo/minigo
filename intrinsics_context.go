package minigo

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"reflect"
	"sync"
	"time"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installContext(h *hostHelpers) {
	// context: bound so script code hands REAL host contexts to bound
	// APIs (net.Dialer.DialContext, http transports) — an interpreted
	// context object cannot satisfy a host context.Context parameter.
	// With* family values ride GoValue boxes; CancelFunc results arrive
	// as plain host funcs.
	tbl := contextGenEntries()
	for k, v := range map[string]runtime.Value{
		"Context": &runtime.TypeDef{Name: "context.Context", Kind: runtime.KindInterface,
			MReqs: []string{"Deadline", "Done", "Err", "Value"}},
		"CancelFunc": &runtime.TypeDef{Name: "context.CancelFunc", Kind: runtime.KindFunc,
			Anon: &ast.FuncType{Params: &ast.FieldList{}}},
		"CancelCauseFunc": &runtime.TypeDef{Name: "context.CancelCauseFunc", Kind: runtime.KindFunc,
			Anon: &ast.FuncType{Params: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("error")}}}}},
		"WithCancel": h.fnvc("context.WithCancel", func(vc runtime.VMCaller, a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithCancel(c)
			watchCtxDone(vc, c, nc)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithCancelCause": h.fnvc("context.WithCancelCause", func(vc runtime.VMCaller, a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithCancelCause(c)
			watchCtxDone(vc, c, nc)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithDeadline": h.fn("context.WithDeadline", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			d, ok := a[1].(time.Time)
			if !ok {
				return nil, fmt.Errorf("context.WithDeadline needs a time.Time, got %T", a[1])
			}
			nc, cancel := context.WithDeadline(c, d)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithDeadlineCause": h.fnvcRaw("context.WithDeadlineCause", 3, func(vc runtime.VMCaller, a []any, raw []runtime.Value) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			d, ok := a[1].(time.Time)
			if !ok {
				return nil, fmt.Errorf("context.WithDeadlineCause needs a time.Time, got %T", a[1])
			}
			nc, cancel := context.WithDeadlineCause(c, d, hostbridge.HostErrOf(vc, raw[2]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithTimeout": h.fn("context.WithTimeout", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithTimeout(c, durOf(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithTimeoutCause": h.fnvcRaw("context.WithTimeoutCause", 3, func(vc runtime.VMCaller, a []any, raw []runtime.Value) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			nc, cancel := context.WithTimeoutCause(c, durOf(a[1]), hostbridge.HostErrOf(vc, raw[2]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(nc), scriptVal(cancel)}}, nil
		}),
		"WithoutCancel": h.fn("context.WithoutCancel", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			return context.WithoutCancel(c), nil
		}, context.WithoutCancel),
		// WithValue stores the key in its canonical form: the valueCtx
		// lookup (opaqueStore's key position) canonicalizes the same
		// way, so equal script keys hit by content and an unhashable key
		// panics like gc's "key is not comparable" — pointer keys keep
		// identity (e.g. http.LocalAddrContextKey).
		"WithValue": &runtime.BuiltinFunc{Name: "context.WithValue", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 3 {
				return nil, fmt.Errorf("context.WithValue needs 3 args, got %d", len(args))
			}
			c, err := asCtx(hostbridge.GoNative(args[0]))
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: context.WithValue(c, contextKey(args[1]), args[2])}, nil
		}},
		"AfterFunc": &runtime.BuiltinFunc{Name: "context.AfterFunc", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("context.AfterFunc needs 2 args, got %d", len(args))
			}
			c, err := asCtx(hostbridge.GoNative(args[0]))
			if err != nil {
				return nil, err
			}
			f := args[1]
			// a pending AfterFunc callback is a wake source for
			// deadlock detection, like a pending gc timer.
			release := vm.NoteExternalWait(vc)
			stop := context.AfterFunc(c, func() {
				release()
				if _, err := vc.Call(f, nil); err != nil {
					// a dead process refuses the spawn — the callback
					// dies with the run like a Go timer's pending call.
					if !vm.IsProcExit(err) {
						panic(err)
					}
				}
			})
			return &runtime.BuiltinFunc{Name: "context.AfterFunc.stop", Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				stopped := stop()
				if stopped {
					release()
				}
				return stopped, nil
			}}, nil
		}},
		"Cause": h.fn("context.Cause", func(a []any) (any, error) {
			c, err := asCtx(a[0])
			if err != nil {
				return nil, err
			}
			return errVal(context.Cause(c)), nil
		}),
	} {
		tbl[k] = v
	}
	e.bindBound("context", tbl)
}

// lockerOf resolves a script value to a sync.Locker for NewCond: host
// values pass through hostbridge.GoNative (*sync.Mutex, *sync.RWMutex), while a
// script-defined value adapts through its Lock/Unlock members — calls
// run back on the VM that built the Cond, the same hazard as every
// retained host callback.
func lockerOf(vc runtime.MemberCaller, v runtime.Value) (sync.Locker, error) {
	if l, ok := hostbridge.GoNative(v).(sync.Locker); ok {
		return l, nil
	}
	lock, lok := runtime.IfaceMember(vc, v, "Lock")
	unlock, uok := runtime.IfaceMember(vc, v, "Unlock")
	if lok && uok {
		return &scriptLocker{vc: vc, lock: lock, unlock: unlock}, nil
	}
	return nil, fmt.Errorf("sync.NewCond: cannot use %T as sync.Locker", v)
}

// scriptCoro emulates runtime's coro for the iter.Pull/Pull2 linknames:
// a rendezvous that always holds exactly one parked goroutine — either
// the seq worker (waiting to run or suspended in yield) or the next/
// stop caller that switched into it.
type scriptCoro struct {
	ch chan struct{}
}

// scriptLocker adapts a script value with Lock/Unlock methods to a host
// sync.Locker so sync.Cond can wait on it.
type scriptLocker struct {
	vc           runtime.MemberCaller
	lock, unlock runtime.Value
}

// syncOnceWrap implements the sync.OnceFunc family: the returned callable
// memoizes the script function's first result under a real host
// sync.Once. The inner call runs on the VM that invokes the wrapper
// (vc.Call inside Fn), and a panic on that first call consumes the once
// like Go's OnceFunc does.
func syncOnceWrap(name string, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
	}
	f := args[0]
	var once sync.Once
	var res runtime.Value
	return &runtime.BuiltinFunc{Name: name + "(...)", Fn: func(vc runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
		once.Do(func() {
			r, err := vc.Call(f, nil)
			if err != nil {
				panic(err)
			}
			res = r
		})
		if res == nil {
			return runtime.NIL, nil
		}
		return res, nil
	}}, nil
}

// contextKey canonicalizes a context.WithValue key: the stored key must
// take the same canonical form the valueCtx lookup produces (the opaque
// store's key position) or equal script keys never hit. An unhashable
// key panics with gc's "key is not comparable" message rather than the
// hash-type error OpaqueKey reports.
func contextKey(k runtime.Value) (key any) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(*runtime.Panic); ok {
				panic(&runtime.Panic{Value: "key is not comparable"})
			}
			panic(r)
		}
	}()
	return runtime.OpaqueKey(k)
}

// asCtx resolves a script value to a host context.Context — bound
// context producers (Background, With*) all yield GoValue boxes, so the
// host value is already unwrapped by hostbridge.GoNative upstream.
func asCtx(v any) (context.Context, error) {
	if v == nil {
		return nil, errors.New("context: nil parent")
	}
	if c, ok := v.(context.Context); ok {
		return c, nil
	}
	return nil, fmt.Errorf("not a context.Context: %T", v)
}

// watchCtxDone registers a cancel-family context's done channel in the
// parked-receive liveness registry: the channel closes only when a
// CancelFunc runs — a goroutine action, never an autonomous send — so a
// receive parked on it counts asleep, like gc's deadlock detection of
// `ctx, _ := context.WithCancel(bg); <-ctx.Done()`.
//
// A parent whose done channel can fire on its own propagates that
// liveness instead: a deadline parent's timer closes the child's done
// host-side without any script goroutine acting. When the parent's
// done is unmanaged or still live the child stays unmanaged (a
// conservative wake source); a nil or dead-managed parent passes the
// cancel-only property down, and the child registers as dead from
// birth.
func watchCtxDone(vc runtime.CallerRef, parent, child context.Context) {
	done := child.Done()
	if done == nil {
		return
	}
	if pdone := parent.Done(); pdone != nil {
		if managed, alive := vm.WakeChanState(vc, reflect.ValueOf(pdone).Pointer()); !managed || alive {
			return
		}
	}
	if ptr := vm.RegisterWakeChan(vc, done); ptr != 0 {
		vm.SetWakeChanDead(vc, ptr, true)
	}
}

func (s *scriptLocker) Lock() { s.call(s.lock) }

func (s *scriptLocker) Unlock() { s.call(s.unlock) }

func (s *scriptLocker) call(fn runtime.Value) {
	if _, err := s.vc.Call(fn, nil); err != nil {
		panic(err)
	}
}
