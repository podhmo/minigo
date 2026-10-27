package main

import "fmt"

// Constraint (core) type inference runs before untyped constants take
// their default type: with `S ~[]E`, `has([]uint8{0}, 0)` learns E=uint8
// from S and the constant 0 converts to it — E is not pinned to int.

func has[S ~[]E, E comparable](s S, v E) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func pick[S ~[]E, E any](s S, v E) E { return v }

func first[E any, S ~[]E](v E, s S) E { return v }

type Bytes []byte

func main() {
	fmt.Println(has([]uint8{0}, 0), has(Bytes("ab"), 'b'))          // true true
	fmt.Println(pick([]float64{1}, 2)/4, first(2, []float64{1})/4)  // 0.5 0.5
	fmt.Printf("%T %T\n", pick([]int8{1}, 2), first(3, []uint16{})) // int8 uint16
}
