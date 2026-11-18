package main

// An instantiated generic spells a named non-nilable type compile
// cannot name statically; the deferred gate resolves it at run time
// and traps like gc.
type Box[T any] struct{ V T }

func main() {
	var b Box[int]
	_ = b == nil
}
