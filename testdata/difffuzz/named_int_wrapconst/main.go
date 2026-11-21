package main

import "fmt"

type K int

func main() {
	n := uint(3)
	var x K = 1
	// the right operand still owes a deferred runtime op (non-constant
	// shift): the named-int fast path must not read its unwrapped V.
	fmt.Println(x + ((1 << n) / 2))
	fmt.Println(x + ((1 << 3) / 2))
	fmt.Println(x == ((1<<n)/2 - 3))
	var y K = 2
	fmt.Println(x + y - ((1 << n) / 2))
}
