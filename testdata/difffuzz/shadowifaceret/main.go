package main

import (
	"fmt"
	"unsafe"
)

type S struct{ N int }
type J interface{ M() }

type T interface{ M2() }

func Id[T any](x T) T { return x }

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
	// Id's result name T collides with the package's `type T
	// interface` — the type-param result is opaque, so z stays
	// int32-flavoured and sizes 4, not the 16-byte iface pair.
	z := Id(int32(5))
	fmt.Println(unsafe.Sizeof(z), z)
}
