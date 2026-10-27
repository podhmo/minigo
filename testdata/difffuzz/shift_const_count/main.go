package main

import (
	"fmt"
	"math/bits"
	"unsafe"
)

// A shift whose count is a constant expression is itself constant when
// the left operand is an untyped constant — whatever form the count
// takes: a typed const decl, typed-const arithmetic, len of a constant
// string or an array, unsafe.Sizeof, min/max of constants, or a
// qualified package constant. The untyped result then adopts the
// context (`(1<<T)/16.0` is the float 0.5, not the int 0), unlike a
// variable count, where the left operand takes the context's type.

const T uint = 3
const c = 1 << T

// math.MaxUint's spelling
const intSize = 32 << (^uint(0) >> 63)
const MaxUint = 1<<intSize - 1

type S struct{ a, b int64 }

func main() {
	fmt.Println(c/16.0, (1<<T)/16.0, 1.5*(1<<T)) // 0.5 0.5 12

	var x uint64 = 12
	fmt.Println(float64(MaxUint), x < MaxUint/3, MaxUint%7) // 1.8446744073709552e+19 true 1

	fmt.Println(1<<bits.UintSize > 0, float64(1<<bits.UintSize)) // true 1.8446744073709552e+19

	var arr [3]int
	fmt.Println((1<<len("abc"))/16.0, (1<<unsafe.Sizeof(S{}))/32.0, (1<<max(T, 3))/16.0, (1<<(T+0))/16.0) // 0.5 2048 0.5 0.5
	fmt.Println((1<<len(arr))/16.0, (1<<cap(arr))/16.0)                                                   // 0.5 0.5

	const L uint = 2
	f := func() float64 { return (1 << L) / 8.0 }
	fmt.Println((1<<L)/8.0, f()) // 0.5 0.5

	// a variable count still types the left operand from the context
	n := uint(3)
	s := []int{1, 2, 3}
	var g float64 = 1 << 3
	fmt.Println(1<<n/16, 1<<len(s)/16, g/16) // 0 0 0.5
}
