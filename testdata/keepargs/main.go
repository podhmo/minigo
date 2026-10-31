package main

import "example.com/keep"

// Main calls the arg-retaining builtin twice on one frame: the second
// call reuses the frame's arg buffer, which rewrites the slice the
// first call stored — unless the builtin declared RetainsArgs and got
// a private copy. The args are variables, not literals: constants
// materialize into a fresh slice on their own.
func Main() {
	a, b := "first", 1
	keep.Hold(a, b)
	keep.Hold("second", 2)
}
