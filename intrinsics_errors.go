package minigo

import (
	"errors"
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installErrors(h *hostHelpers) {
	e.Bind("errors", map[string]runtime.Value{
		"New": h.fn("errors.New", func(a []any) (any, error) { return errors.New(str(a[0])), nil }, errors.New),
		// sentinel vars ride GoValue boxes so `err == errors.ErrUnsupported`
		// and errors.Is against the chain see the real objects.
		"ErrUnsupported": &runtime.GoValue{V: errors.ErrUnsupported},
		"Join": &runtime.BuiltinFunc{Name: "errors.Join", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			var errs []error
			for _, x := range args {
				if err := hostbridge.HostErrOf(v, x); err != nil {
					errs = append(errs, err)
				}
			}
			return errVal(errors.Join(errs...)), nil
		}},
		// Is/Unwrap/As take raw runtime args: h.fn's hostbridge.GoNative would flatten
		// a script error value to its printed string, losing the type the
		// chain walk needs. hostErrOf keeps the script value inside a
		// scriptError wrapper instead.
		"Is": &runtime.BuiltinFunc{Name: "errors.Is", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.Is needs 2 args")
			}
			e0, e1 := hostbridge.HostErrOf(v, args[0]), hostbridge.HostErrOf(v, args[1])
			if e0 == nil || e1 == nil {
				return e0 == e1, nil
			}
			// script-side walk — host errors.Is's `==` compares error
			// pointers and only walks Unwrap() error, so it misses a
			// scriptError's canonical equality and its []error kids.
			// No `err == target` fast path here: gc only compares when
			// the target is comparable, which is errIs's own gate — a
			// pointer-identity shortcut would answer true on a same-box
			// uncomparable target where gc skips `==` entirely.
			return hostbridge.ErrIs(e0, e1), nil
		}},
		"Unwrap": &runtime.BuiltinFunc{Name: "errors.Unwrap", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.Unwrap needs 1 arg")
			}
			return errVal(errors.Unwrap(hostbridge.HostErrOf(v, args[0]))), nil
		}},
		// As walks the error tree the way gc's errors.As does — each
		// node's dynamic type, its own As(any) bool, then Unwrap() error
		// and Unwrap() []error children depth-first — and assigns the
		// first cause satisfying the target cell's declared element type.
		"As": &runtime.BuiltinFunc{Name: "errors.As", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("errors.As needs 2 args")
			}
			want := cellElemTyp(args[1])
			sv, ok := hostbridge.FindAsTarget(v, hostbridge.HostErrOf(v, args[0]), want, e.ifaceReqSet(want), args[1])
			if ok && runtime.SetRef(args[1], sv) {
				return true, nil
			}
			return false, nil
		}},
		// AsType is the generic form of As: GenFn makes the bind
		// instantiable (`errors.AsType[*E](err)` → a plain builtin that
		// returns the first matching chain element with its bool). The
		// walk shares As's matching rule: an interface E accepts any
		// error, a concrete E matches the element's declared typedef.
		"AsType": &runtime.BuiltinFunc{Name: "errors.AsType", GenFn: func(v runtime.VMCaller, targs []runtime.Value, args []runtime.Value) (runtime.Value, error) {
			if len(targs) != 1 {
				return nil, fmt.Errorf("errors.AsType needs 1 type argument")
			}
			if len(args) != 1 {
				return nil, fmt.Errorf("errors.AsType needs 1 arg")
			}
			want, _ := targs[0].(*runtime.TypeDef)
			// gc returns E's zero on a miss — a typed nil so a
			// pointer-receiver Error() on the result still runs.
			var zero runtime.Value = runtime.NIL
			if want != nil {
				zero = v.Zero(want)
			}
			// target stands in for errors.As's user cell: a node's own
			// As method writes its answer through this pointer.
			target := &runtime.Cell{Elem: zero}
			sv, ok := hostbridge.FindAsTarget(v, hostbridge.HostErrOf(v, args[0]), want, e.ifaceReqSet(want), target)
			if ok {
				return &runtime.Tuple{Elems: []runtime.Value{sv, true}}, nil
			}
			return &runtime.Tuple{Elems: []runtime.Value{zero, false}}, nil
		}},
	})
}

// errKids returns the error node's children: a script `Unwrap()
// []error` fans out like errors.Join's elements (a shape Unwrap()
// error cannot express alongside it on one Go type), a plain
// `Unwrap() error` is the single cause.
// wrapError is fmt.Errorf's %w product: message plus one cause.
type wrapError struct {
	msg string
	err error
}

// wrapOperand is gc's `a[n].(error)` on a %w operand: a boxed host
// error, a script value with an Error method, or a boxed typed nil
// (a non-nil error) converts through hostErrOf; anything else — an
// int, a string, a nil interface — wraps nothing.
func wrapOperand(c runtime.MemberCaller, v runtime.Value) error {
	switch x := runtime.Unwrap(v).(type) {
	case *runtime.GoValue:
		if _, ok := x.V.(error); !ok {
			return nil
		}
		return hostbridge.HostErrOf(c, v)
	case *runtime.IfaceNil:
		return hostbridge.HostErrOf(c, v)
	}
	if _, ok := runtime.IfaceMember(c, v, "Error"); ok {
		return hostbridge.HostErrOf(c, v)
	}
	return nil
}

// wrapErrors is fmt.Errorf's product for several %w verbs: message plus
// every cause, reachable through Unwrap() []error.
type wrapErrors struct {
	msg  string
	errs []error
}

// osErrPred binds one of os.IsNotExist/IsExist/IsPermission/IsTimeout.
// These look through exactly one *fs.PathError / *os.LinkError /
// *os.SyscallError layer by host type switch, so the argument goes
// through hostErrArg: a script-built *fs.PathError (io/fs runs from
// source) reaches the host as a real *fs.PathError.
func osErrPred(name string, pred func(error) bool) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: pred, Fn: func(c runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		return pred(hostbridge.HostErrArg(c, args[0])), nil
	}}
}

func (e *wrapError) Error() string { return e.msg }

func (e *wrapError) Unwrap() error { return e.err }

func (e *wrapErrors) Error() string { return e.msg }

func (e *wrapErrors) Unwrap() []error { return e.errs }
