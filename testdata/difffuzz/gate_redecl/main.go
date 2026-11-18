package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_redecl/inner"
)

func zero[T any](v T) T {
	var z T
	return z
}

func main() {
	// x keeps its declared any type across the short redeclaration —
	// the deferred result type of inner.Pair must not override it.
	var x any
	x, ok := inner.Pair()
	_ = ok
	fmt.Println(zero(x))
}
