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
