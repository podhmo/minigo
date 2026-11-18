package main

// COMPILE_TIME_FACT: range elem stamps the declared interface, not the local shadow

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_typeshadow/inner"
)

// shadows inner.I for name resolution in this file; the range
// element is still declared inner.I
type I interface{ Bogus() }

func main() {
	for _, v := range inner.Items {
		fmt.Println(v.M())
	}
}
