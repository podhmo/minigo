package main

import "fmt"

// The same rule through an assignment: `var h float64 = 1 << n` gives
// the shifted untyped 1 the type float64, and a shift needs an integer
// operand — a gc compile error, not 4.

func main() {
	n := uint(2)
	var h float64 = 1 << n
	fmt.Println(h)
}
