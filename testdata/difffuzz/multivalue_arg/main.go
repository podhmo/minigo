package main

import (
	"fmt"
	"slices"
)

// a multi-valued call is a valid argument only when it is the whole
// argument list — gc rejects f("x", g()) as "multiple-value g() in
// single-value context"; minigo must trap, not pass the tuple along.
func main() {
	fmt.Println("x", slices.BinarySearch([]int{1, 3}, 3))
}
