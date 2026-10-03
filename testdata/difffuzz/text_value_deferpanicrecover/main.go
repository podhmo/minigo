package main

import "fmt"

// a deferred call that panics after an earlier deferred call consumed
// the unwinding panic: the new panic supersedes — it must propagate,
// not be swallowed nor left in inflight for a later recover() to find.
func f() {
	defer func() { panic("E2") }() // runs second
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered:", r)
		}
	}() // runs first, consumes P
	panic("P")
}

func h() {
	defer func() { fmt.Println("h sees:", recover()) }()
}

func main() {
	func() {
		defer func() { fmt.Println("outer:", recover()) }()
		f()
	}()
	h()
}
