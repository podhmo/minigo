package main

import "fmt"

// a declared untyped string const keeps its lazy *runtime.UConst box
// past the call boundary — every builtin position that accepts a
// string must materialize it. `decodeMapInitialize` is the
// encoding/base32 shape this divergence blocked.
const decodeMapInitialize = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func main() {
	// copy(dst, pkgConstString) behaves like copy(dst, "lit").
	var a [4]uint8
	fmt.Println(copy(a[:], decodeMapInitialize))
	fmt.Printf("%q\n", a[:])

	// a function-local untyped const is the same lazy box.
	const c = "ab"
	var b [4]uint8
	fmt.Println(copy(b[:], c))

	// copy into a []byte dst, not only an array slice.
	d := []byte{0, 0, 0}
	fmt.Println(copy(d, c), string(d))

	// append(b, const...) spreads the const string's bytes.
	e := append([]byte("x"), c...)
	fmt.Println(string(e))

	// a nil dst copies 0 elements — the const takes the same
	// string-source path as the literal, without touching dst.Typ.
	var nilb []byte
	fmt.Println(copy(nilb, c))
	fmt.Println(copy(nilb, "ab"))
}
