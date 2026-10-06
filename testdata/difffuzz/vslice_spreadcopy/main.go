package main

import "fmt"

// Spread and copy on virtual zero-size slices must use the logical
// length, not the (never materialized) element count.

func main() {
	s := make([]struct{}, 1<<33)
	fmt.Println(len(append(s[:1], s...)))
	fmt.Println(copy(s, s[:2]))
}
