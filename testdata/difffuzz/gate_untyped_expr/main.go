package main

import "fmt"

type D int

// x := <untyped const expr> binds the constant's default type like gc —
// `x == d` is a compile error (mismatched types int and D), not false.
func main() {
	var d D
	x := 1 + 2
	fmt.Println(x == d)
}
