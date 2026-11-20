package main

import (
	"fmt"
	"slices"
)

func main() {
	// gc: cannot use 300 (untyped int constant) as uint8 value —
	// a constant operand must be representable as the element type.
	fmt.Println(slices.Index([]uint8{}, 300))
}
