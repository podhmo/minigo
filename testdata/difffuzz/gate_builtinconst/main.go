package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_builtinconst/inner"
)

// Untyped builtin-derived consts compare against named numeric types —
// a gate that misreads them as typed would reject each of these.
type F float64

func main() {
	var f F = 2
	var g F
	var c complex128 = 1 + 2i
	fmt.Println(inner.I == f, inner.R == g, inner.C == c)
}
