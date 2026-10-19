package runtime

import "fmt"

// Panic builders for Go's runtime-error families. Each constructor
// produces both the Panic wrapper and the payload type Go uses for that
// failure, so a script's `recover().(error)` and its .Error() text match
// the real toolchain — plain payloads must never carry the
// "runtime error:" prefix that RuntimeError.Error() adds.

// RuntimePanic returns a [Panic] whose value is a [RuntimeError] with
// msg; its text is "runtime error: <msg>", as for Go's run-time errors.
func RuntimePanic(msg string) *Panic {
	return &Panic{Value: &RuntimeError{Msg: msg}}
}

// RuntimePanicKind is [RuntimePanic] with [RuntimeError.Kind] set to
// kind, the Go runtime type the error stands for (such as
// "boundsError").
func RuntimePanicKind(msg, kind string) *Panic {
	return &Panic{Value: &RuntimeError{Msg: msg, Kind: kind}}
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

// PlainPanic returns a [Panic] whose value is a [PlainError] with msg,
// for run-time errors Go reports without the "runtime error:" prefix.
func PlainPanic(msg string) *Panic {
	return &Panic{Value: PlainError(msg)}
}

// NilDerefPanic is Go's "invalid memory address or nil pointer
// dereference" runtime error.
func NilDerefPanic() *Panic {
	return RuntimePanic("invalid memory address or nil pointer dereference")
}

// BoundsPanic is Go's panic for an index i out of range of length n:
// "index out of range [i] with length n", or "index out of range [i]"
// for a negative int or int64 i.
func BoundsPanic(i any, n int) *Panic {
	switch iv := i.(type) {
	case int64:
		if iv < 0 {
			return RuntimePanicKind(fmt.Sprintf("index out of range [%v]", i), "boundsError")
		}
	case int:
		if iv < 0 {
			return RuntimePanicKind(fmt.Sprintf("index out of range [%v]", i), "boundsError")
		}
	}
	return RuntimePanicKind(fmt.Sprintf("index out of range [%v] with length %d", i, n), "boundsError")
}

// SliceBoundsPanic is Go's panic for slice bounds out of range; msg is
// the complete message ("slice bounds out of range ...").
func SliceBoundsPanic(msg string) *Panic {
	return RuntimePanicKind(msg, "boundsError")
}

// ComparingUncomparablePanic is Go's "comparing uncomparable type T"
// panic; typ is the type's display name.
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

// NilMapAssignPanic is Go's "assignment to entry in nil map" panic.
func NilMapAssignPanic() *Panic {
	return PlainPanic("assignment to entry in nil map")
}

// CloseNilChanPanic is Go's "close of nil channel" panic.
func CloseNilChanPanic() *Panic {
	return PlainPanic("close of nil channel")
}

// MakeslicePanic is Go's "makeslice: len out of range" panic; what is
// "len" or "cap".
func MakeslicePanic(what string) *Panic {
	return RuntimePanic("makeslice: " + what + " out of range")
}
