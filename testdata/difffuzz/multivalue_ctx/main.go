package main

import "fmt"

func g() (int, bool) { return 1, true }

func main() {
	// a multi-value call as a literal element is a single-value context —
	// gc rejects it like a non-lone call argument.
	fmt.Println([]any{0, g()})
}
