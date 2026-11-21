package stdlib

import (
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// WrapError is fmt.Errorf's %w product: message plus one cause.
type WrapError struct {
	Msg string
	Err error
}

// WrapOperand is gc's `a[n].(error)` on a %w operand: a boxed host
// error, a script value with an Error method, or a boxed typed nil
// (a non-nil error) converts through hostErrOf; anything else — an
// int, a string, a nil interface — wraps nothing.
func WrapOperand(c runtime.MemberCaller, v runtime.Value) error {
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

// WrapErrors is fmt.Errorf's product for several %w verbs: message plus
// every cause, reachable through Unwrap() []error.
type WrapErrors struct {
	Msg  string
	Errs []error
}

func (e *WrapError) Error() string  { return e.Msg }
func (e *WrapError) Unwrap() error  { return e.Err }
func (e *WrapErrors) Error() string { return e.Msg }
func (e *WrapErrors) Unwrap() []error {
	return e.Errs
}

// OsErrPred binds one of os.IsNotExist/IsExist/IsPermission/IsTimeout.
// These look through exactly one *fs.PathError / *os.LinkError /
// *os.SyscallError layer by host type switch, so the argument goes
// through hostErrArg: a script-built *fs.PathError (io/fs runs from
// source) reaches the host as a real *fs.PathError.
func OsErrPred(name string, pred func(error) bool) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: pred, Fn: func(c runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s needs 1 arg, got %d", name, len(args))
		}
		return pred(hostbridge.HostErrArg(c, args[0])), nil
	}}
}
