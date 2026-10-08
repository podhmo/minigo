package main

import "strings"

// BoomViaBuiltin fails inside a host builtin — the traceback should name
// strings.Repeat itself as a "(builtin)" frame.
func BoomViaBuiltin() string {
	return strings.Repeat("a", -1)
}

// Id is a generic function; a panic inside should render as Id[int]().
func Id[T any](x T) T { panic("in generic") }

// Add takes two params; calling it with one arg must trap, not bind nil.
func Add(a, b int) int { return a + b }

// boom panics unconditionally.
func boom() {
	panic("kaboom")
}

// Wrap calls boom so the traceback spans two script frames.
func Wrap() { boom() }

// Idx faults on a host-level index-out-of-range (a Go panic inside the
// VM, lifted into a script panic).
func Idx() int {
	xs := []int{1}
	return xs[10]
}

// Unsupported hits an OpTrap (x.(type) outside a type switch is not
// supported).
func Unsupported() {
	var x any = 1
	_ = x.(type)
}

// DeferredCleanup panics from a deferred call; the traceback names the
// frame that registered the defer via a "(deferred call)" entry.
func DeferredCleanup() { panic("in defer") }

func WithDefer() {
	defer DeferredCleanup()
}

// Repanic recovers a panic and re-panics with it, like go/parser's
// bailout handler; the traceback keeps the original panic site below
// the deferred call, as Go's does.
func Repanic() {
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
	}()
	repanicOrigin()
}

func repanicOrigin() {
	var m map[string]int
	m["x"] = 1
}

// RepanicChain repanics after recovering: the fatal render chains the
// superseded panic first with gc's ` [recovered]` marker, then the new
// panic under a leading tab.
func RepanicChain() {
	defer func() {
		recover()
		panic("chain-head")
	}()
	panic("chain-tail")
}

// RepanicPlain repanics WITHOUT recovering: the chain still links but
// carries no `[recovered]` marker.
func RepanicPlain() {
	defer func() { panic("chain-new") }()
	panic("chain-old")
}

// --- panic payload shapes (gc's printpanics contract) ---

// panicError implements error on the pointer receiver.
type panicError struct{ msg string }

func (e *panicError) Error() string { return e.msg }

// panicStringer implements fmt.Stringer.
type panicStringer struct{}

func (s panicStringer) String() string { return "stringer text" }

// panicScalar is a package-declared named scalar — gc prints its panic
// payload as `main.panicScalar(v)`.
type panicScalar int

// panicPoint is a plain struct: its panic payload has no method text,
// so gc renders `(type) 0xADDR`.
type panicPoint struct{ x, y int }

// sigError shares error's method NAME without its signature — an
// `Error() any` method does not implement error in gc, so the payload
// renders `(main.sigError) 0xADDR` and the method is never invoked.
type sigError struct{}

func (sigError) Error() any { return "not an error" }

// sigErrorParams declares Error with a parameter — likewise not the
// interface method.
type sigErrorParams struct{}

func (sigErrorParams) Error(n int) string { return "not an error" }

// sigStringer shares fmt.Stringer's method name without its
// signature.
type sigStringer struct{}

func (sigStringer) String() any { return "not a stringer" }

// PanicErrorValue panics with a script error pointer — the fatal render
// calls its Error() instead of dumping the value.
func PanicErrorValue() { panic(&panicError{msg: "script error text"}) }

// PanicErrorValueRecv panics with a bare struct whose Error() is on the
// pointer receiver — a value does not implement error, so gc renders
// `(main.panicError) 0xADDR`.
func PanicErrorValueRecv() { panic(panicError{msg: "x"}) }

// PanicStringer panics with a fmt.Stringer payload — the fatal render
// calls String().
func PanicStringer() { panic(panicStringer{}) }

// PanicNamedScalar panics with a package-declared named scalar — gc
// prints `main.panicScalar(3)`.
func PanicNamedScalar() { panic(panicScalar(3)) }

// PanicStruct and PanicStructPtr panic with method-less composites —
// gc renders `(type) 0xADDR`.
func PanicStruct()    { panic(panicPoint{x: 1, y: 2}) }
func PanicStructPtr() { panic(&panicPoint{x: 1, y: 2}) }
func PanicSlice()     { panic([]int{1, 2}) }

// PanicErrorBadSig / PanicErrorBadParams / PanicStringerBadSig panic
// with payloads whose Error/String method does not match the
// `func() string` signature gc requires — the interface is not
// satisfied, so gc renders `(type) 0xADDR` without calling it.
func PanicErrorBadSig()    { panic(sigError{}) }
func PanicErrorBadParams() { panic(sigErrorParams{}) }
func PanicStringerBadSig() { panic(sigStringer{}) }

// PanicErrorChain repanics over a script-error payload: the chain keeps
// the [recovered] marker on the Error() text.
func PanicErrorChain() {
	defer func() {
		recover()
		panic("chain-head")
	}()
	panic(&panicError{msg: "chain-tail"})
}
