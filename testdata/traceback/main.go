package main

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

// Unsupported hits an OpTrap (3-index slice is not supported).
func Unsupported() {
	s := []int{1, 2, 3}
	_ = s[0:1:2]
}

// DeferredCleanup panics from a deferred call; the traceback names the
// frame that registered the defer via a "(deferred call)" entry.
func DeferredCleanup() { panic("in defer") }

func WithDefer() {
	defer DeferredCleanup()
}
