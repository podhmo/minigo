package main

import (
	"fmt"
	"unsafe"
)

// Sizes and alignments come from the DECLARED type's amd64 layout —
// struct fields pad per their declared widths (St: int8 padded to
// int64 = 16, not the unpadded 9), an array sizes elem*N, scalars by
// width, complex64 = 8 / complex128 = 16.
type St struct {
	A int8
	B int64
}
type Arr [3]int16
type P struct {
	C complex64
	D int16
	E int64
}

func main() {
	var a int8 = 1
	var c complex64 = 0
	var s St
	var arr Arr
	fmt.Println(unsafe.Sizeof(a), unsafe.Sizeof(c), unsafe.Sizeof(s),
		unsafe.Sizeof(arr), unsafe.Sizeof(int16(0)), unsafe.Sizeof('x'),
		unsafe.Sizeof(complex128(0)), unsafe.Sizeof(St{}), unsafe.Sizeof(P{}))
	fmt.Println(unsafe.Alignof(a), unsafe.Alignof(s), unsafe.Alignof(arr),
		unsafe.Alignof(int16(0)), unsafe.Alignof(P{}))
	fmt.Println(unsafe.Offsetof(P{}.E))
}
