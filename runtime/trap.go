package runtime

import (
	"fmt"
	"go/token"
)

// TrapCode is the stable machine-readable category of a [Trap]. The
// taxonomy is intentionally small: use the generic runtime code rather
// than adding a narrow category before a consumer needs it.
type TrapCode string

const (
	// TrapCodeRuntime is a dynamic semantic failure with no more specific
	// stable category. It is also the compatibility default for a Trap
	// whose Code was never set.
	TrapCodeRuntime TrapCode = "MG-RUNTIME"
	// TrapCodeUndefined is a missing script or host member: an undefined
	// identifier, a missing package member, or a missing field or method.
	TrapCodeUndefined TrapCode = "MG-UNDEF"
	// TrapCodeUnsupported is a Go construct or type operation the
	// interpreter recognizes but does not implement.
	TrapCodeUnsupported TrapCode = "MG-UNSUPPORTED"
	// TrapCodeHostConversion is a failed value conversion across the
	// script/host boundary.
	TrapCodeHostConversion TrapCode = "MG-HOSTCONV"
)

// TrapSpec is the immutable OpTrap constant payload emitted by the
// compiler; the VM materializes it into a fresh Trap at execution.
type TrapSpec struct {
	Code   TrapCode
	Reason string
}

// UndefinedError marks a resolution failure as [TrapCodeUndefined] when
// it becomes a trap. It keeps that classification across `%w` wrapping.
type UndefinedError struct{ Reason string }

func (e *UndefinedError) Error() string { return e.Reason }

// Trap is a failure the script cannot recover(), unlike a [Panic]: an
// unsupported construct, an invalid operation, or an error returned by a
// [BuiltinFunc] or [SpecialFunc]. Deferred calls run as it unwinds, but
// recover() in them returns nil and the Trap continues. It unwinds to the
// host as the error of [github.com/podhmo/minigo.Engine.Call]; Err,
// reachable with errors.As, is the host error it was raised from.
type Trap struct {
	Pos    token.Pos
	Code   TrapCode
	Reason string
	Frames []string // "func at file:line" entries collected while unwinding
	// Err is the host error the trap was raised from (a special-form
	// handler, builtin or host call that returned an error), or nil for
	// a trap the VM raised itself. Reason is its text; keeping the value
	// lets hosts errors.As their own typed errors through the trap.
	Err error
}

// StableCode returns the trap's machine-readable code, treating the zero
// value as the generic runtime code for traps constructed before the
// taxonomy (or directly by a host).
func (t *Trap) StableCode() TrapCode {
	if t.Code != "" {
		return t.Code
	}
	return TrapCodeRuntime
}

// Unwrap exposes the host error a trap was raised from.
func (t *Trap) Unwrap() error { return t.Err }

func (t *Trap) Error() string {
	if len(t.Frames) == 0 {
		return fmt.Sprintf("runtime trap: %s", t.Reason)
	}
	return fmt.Sprintf("runtime trap: %s\nTraceback (most recent call first):\n%s", t.Reason, renderFrames(t.Frames))
}
