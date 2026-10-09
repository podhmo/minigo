package runtime

import "fmt"

// Panic builders for Go's runtime-error families. Each constructor
// produces both the Panic wrapper and the payload type Go uses for that
// failure, so a script's `recover().(error)` and its .Error() text match
// the real toolchain — plain payloads must never carry the
// "runtime error:" prefix that RuntimeError.Error() adds.

// RuntimePanic wraps msg in a Go runtime error payload; Error() renders
// it as "runtime error: <msg>" like Go's errorString/boundsError.
func RuntimePanic(msg string) *Panic {
	return &Panic{Value: &RuntimeError{Msg: msg}}
}

// PlainError is Go's runtime.plainError: an error whose Error() text is
// the bare message with no "runtime error:" prefix.
type PlainError string

func (e PlainError) Error() string { return string(e) }

// RuntimeError marks PlainError as a runtime.Error, like Go's plainError.
func (PlainError) RuntimeError() {}

// TypeAssertionError is Go's *runtime.TypeAssertionError payload: a
// failed x.(T), whose Error() text is the "interface conversion: ..."
// message.
type TypeAssertionError struct{ Msg string }

func (e *TypeAssertionError) Error() string { return e.Msg }

// RuntimeError marks a failed type assertion as a runtime.Error.
func (*TypeAssertionError) RuntimeError() {}

// PlainPanic wraps msg in a plainError payload (nil-map assignment,
// close of a nil channel, ...).
func PlainPanic(msg string) *Panic {
	return &Panic{Value: PlainError(msg)}
}

// NilDerefPanic is Go's "invalid memory address or nil pointer
// dereference" runtime error.
func NilDerefPanic() *Panic {
	return RuntimePanic("invalid memory address or nil pointer dereference")
}

// BoundsPanic is Go's boundsError for a linear index: "index out of
// range [i] with length n" — a negative index drops the length suffix,
// like Go's bare boundsNegErrorFmts report.
func BoundsPanic(i any, n int) *Panic {
	switch iv := i.(type) {
	case int64:
		if iv < 0 {
			return RuntimePanic(fmt.Sprintf("index out of range [%v]", i))
		}
	case int:
		if iv < 0 {
			return RuntimePanic(fmt.Sprintf("index out of range [%v]", i))
		}
	}
	return RuntimePanic(fmt.Sprintf("index out of range [%v] with length %d", i, n))
}

// ComparingUncomparablePanic is Go's "comparing uncomparable type T".
func ComparingUncomparablePanic(typ string) *Panic {
	return RuntimePanic("comparing uncomparable type " + typ)
}

// NegativeShiftPanic is Go's "negative shift amount".
func NegativeShiftPanic() *Panic {
	return RuntimePanic("negative shift amount")
}

// SliceToArrayPanic is Go's conversion failure for a slice whose length
// doesn't match the target array length.
func SliceToArrayPanic(have, want any) *Panic {
	return RuntimePanic(fmt.Sprintf(
		"cannot convert slice with length %d to array or pointer to array with length %d", have, want))
}

// NilMapAssignPanic is Go's plainError for writing to a nil map.
func NilMapAssignPanic() *Panic {
	return PlainPanic("assignment to entry in nil map")
}

// CloseNilChanPanic is Go's plainError for close(nil-channel).
func CloseNilChanPanic() *Panic {
	return PlainPanic("close of nil channel")
}

// MakeslicePanic is Go's "makeslice: len|cap out of range".
func MakeslicePanic(what string) *Panic {
	return RuntimePanic("makeslice: " + what + " out of range")
}
