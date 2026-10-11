package main

import "fmt"

type D int

// s[i] on a string yields a byte — `s[0] == d` is a compile error
// (mismatched types byte and D), not a runtime index panic.
func main() {
	var d D
	var s string
	fmt.Println(s[0] == d)
}
