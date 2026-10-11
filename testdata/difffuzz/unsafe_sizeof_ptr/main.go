package main

import (
	"fmt"
	"unsafe"
)

// A pointer-typed operand answers the POINTER's layout (8/8), never
// the pointee's: `&st` and a `*[100]byte` variable arrive as the
// addressable cell itself, whose stamped tag is the pointee's declared
// type — reporting it directly would size the pointee.
type St struct {
	A int
	B byte
}

type MyPtr *St

func main() {
	var arr [100]byte
	var p *[100]byte = &arr
	var st St
	var mp MyPtr = &st
	var p2 *St = &st
	var pp **St = &p2
	var i interface{ M() string }
	fmt.Println(unsafe.Sizeof(p), unsafe.Sizeof(&st),
		unsafe.Sizeof(mp), unsafe.Sizeof(pp), unsafe.Sizeof(new(St)))
	fmt.Println(unsafe.Alignof(&arr), unsafe.Alignof(&st),
		unsafe.Alignof(&arr[0]), unsafe.Alignof(&i))
	// declared-type cases still answer by layout
	fmt.Println(unsafe.Sizeof(st), unsafe.Sizeof(arr), unsafe.Alignof(arr[0]))
}
