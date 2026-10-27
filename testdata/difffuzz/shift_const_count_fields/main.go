package main

import (
	"fmt"
	"unsafe"
)

// Constant shift counts the first static walk missed: len of an array
// field read through a pointer or an anonymous struct, and
// unsafe.Sizeof of a non-generic operand inside a generic body. The
// untyped 1 then stays a constant and adopts the float operand — no
// "shifted operand must be integer" rejection. A typed left operand
// (`int(1) << n`) shifts as an int whatever the count.

type W struct{ a [3]int }

func gen[T any](f float64) float64 {
	var i int
	return f * (1 << unsafe.Sizeof(i))
}

func main() {
	var f float64 = 0.5
	w := &W{}
	fmt.Println(f * (1 << len(w.a))) // 4
	var s struct{ b [3]int }
	fmt.Println(f * (1 << len(s.b)))       // 4
	fmt.Println(gen[int](f) / 256)         // 0.5
	fmt.Println(1 << len(w.a) >> len(w.a)) // 1
	n := uint(3)
	fmt.Println(float64(int(1)<<n), f*float64(int(2)<<n)) // 8 8
}
