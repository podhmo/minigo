package main

import (
	"fmt"
	"unsafe"
)

// X's B offset must come from the DECLARED type of A (an interface
// pair, 16 bytes), not the size of whatever A holds at run time.
type X struct {
	A any
	B int
}

type inner struct {
	R byte
	S int64
}

// N nests a declared struct: inner is 16 bytes (byte padded to the
// int64's alignment), so T lands at 24, not 24-with-runtime-sizes.
type N struct {
	P int32
	Q inner
	T byte
}

type Emb struct {
	Z int8
	N
	W uintptr
}

func main() {
	x := X{A: int64(7), B: 2}
	fmt.Println(unsafe.Offsetof(x.A), unsafe.Offsetof(x.B))
	x.A = "hi" // a different payload must not move B's offset
	fmt.Println(unsafe.Offsetof(x.B))
	var n N
	fmt.Println(unsafe.Offsetof(n.P), unsafe.Offsetof(n.Q), unsafe.Offsetof(n.T))
	var em Emb
	fmt.Println(unsafe.Offsetof(em.Z), unsafe.Offsetof(em.N), unsafe.Offsetof(em.W))
	fmt.Println(unsafe.Offsetof(struct {
		A any
		B int
	}{A: true, B: 1}.B))
}
