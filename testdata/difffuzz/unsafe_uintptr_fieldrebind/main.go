package main

import (
	"fmt"
	"unsafe"
)

type S struct {
	N  int
	EP *E
}

type E struct{ N int }

func readN(p unsafe.Pointer) int {
	return *(*int)(p)
}

func main() {
	var s S
	s.N = 1
	s.EP = &E{N: 5}
	u := uintptr(unsafe.Pointer(&s.N))
	w := uintptr(unsafe.Pointer(&s.EP.N))
	// whole-struct rebind overwrites s's storage in place — &s.N
	// tracks the new field value.
	s = S{N: 2, EP: &E{N: 7}}
	fmt.Println(readN(unsafe.Pointer(u)))
	// &s.EP.N named the OLD heap E — rebinding s.EP must not
	// retarget it.
	fmt.Println(readN(unsafe.Pointer(w)))
}
