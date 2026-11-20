package main

import "fmt"

// A divisor that reads zero in the tagged domain is a compile-time
// division by zero in gc — `1e-50` rounds to float32 zero — so the
// folded const op must trap, not mint +Inf through the runtime path.
func main() {
	fmt.Println(float32(1) / 1e-50)
}
