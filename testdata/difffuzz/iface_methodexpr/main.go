package main

import "fmt"

type I interface{ M() int }

func main() {
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	// I.M is a method expression; calling it on a nil interface must
	// panic with nil pointer dereference, not trap.
	f := I.M
	f(nil)
}
