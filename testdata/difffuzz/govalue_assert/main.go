package main

import (
	"bytes"
	"fmt"
)

func main() {
	// complex64 boxes as a host value; it must assert back to complex64.
	var a any = complex64(complex(float32(1.5), float32(0.5)))
	c := a.(complex64)
	fmt.Println(real(c), imag(c))

	// host struct boxes assert to their type
	var b any = bytes.Buffer{}
	if _, ok := b.(bytes.Buffer); ok {
		fmt.Println("buf ok")
	}

	// negative: complex64 box does not assert to complex128
	if _, ok := a.(complex128); !ok {
		fmt.Println("not 128")
	}
}
