package main

import (
	"fmt"
	"unsafe"
)

// unsafe.Sizeof/Alignof answer from the operand's DECLARED type — an
// interface-typed variable measures the interface pair (16), never the
// stored value, and an interface-typed element/entry the same. The
// compiler attaches the operand's static typedef to the call; an
// indexed operand sizes the container's element type.
type S struct {
	A int
	B [100]byte
}

type I interface{ M() string }

func (s S) M() string { return "s" }

var G any = int64(1)

func f(v any) uintptr { return unsafe.Sizeof(v) + unsafe.Alignof(v) }

func main() {
	var x any = int64(1)
	var i I = S{A: 1}
	var st S
	var y any
	var z any = &st
	var w any = []int{1, 2}
	fmt.Println(unsafe.Sizeof(x), unsafe.Sizeof(i), unsafe.Sizeof(y), unsafe.Sizeof(z), unsafe.Sizeof(w))
	fmt.Println(unsafe.Alignof(x), unsafe.Alignof(i), unsafe.Alignof(y), unsafe.Alignof(z), unsafe.Alignof(w))
	var iarr [2]I = [2]I{S{A: 1}, S{A: 2}}
	sl := []I{S{A: 3}}
	m := map[string]I{"k": S{A: 9}}
	var parr *[3]int = &[3]int{1, 2, 3}
	fmt.Println(unsafe.Sizeof(iarr[0]), unsafe.Sizeof(sl[0]), unsafe.Sizeof(m["k"]), unsafe.Sizeof(parr[0]))
	fmt.Println(unsafe.Sizeof(G), f(x), unsafe.Sizeof(st.B))
}
