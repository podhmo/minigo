package main

import (
	"fmt"
	"unsafe"
)

func take(s []int) {
	fmt.Println(s)
}

// unsafe.Slice over an array element pointer yields the unnamed []E —
// the array type [N]E must not leak into the result.
func main() {
	a := [4]int{1, 2, 3, 4}
	s := unsafe.Slice(&a[0], 2)
	fmt.Printf("%T %d %d\n", s, len(s), cap(s))
	take(s)
	s[0] = 9
	fmt.Println(a[0])
}
