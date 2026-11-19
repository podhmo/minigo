package main

import (
	"fmt"
	"unsafe"
)

type S struct{ N int }
type J interface{ M() }

func NewS() S { return S{N: 7} }

var fs = map[int]func() S{0: NewS}

var PI J

func main() {
	// `type S` shadows the package decl inside main: the signature's
	// result name still resolves to package S — not the local
	// interface — so sizeof stays the struct's size.
	type S interface{ M() }
	type J struct{ N int }
	var _ S = nil
	var _ = J{}
	x := fs[0]()
	fmt.Println(unsafe.Sizeof(x), x.N)
	y := NewS()
	fmt.Println(unsafe.Sizeof(y), y.N)
	// PI's declared type is package J — an interface — so it
	// compares nil like any interface value.
	fmt.Println(PI == nil)
}
