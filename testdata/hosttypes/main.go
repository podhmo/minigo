package main

import (
	"fmt"
	"slices"

	ht "example.com/hosttypes"
)

func main() {
	ht.Grab([]any{int8(5), int64(9), "x", 1.5})
	slices.Reverse(boxed())
	fmt.Println(ht.Show())
}
func boxed() []any { return nil }
