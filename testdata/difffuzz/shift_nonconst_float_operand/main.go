package main

import "fmt"

// An untyped constant shifted by a non-constant count takes the other
// operand's type, which must be an integer type: with a float64 f,
// `f + 1<<n` is gc's "invalid operation: shifted operand 1 (type
// float64) must be integer" — not 6.5.

func main() {
	n := uint(2)
	var f float64 = 2.5
	fmt.Println(f + 1<<n)
}
