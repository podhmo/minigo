package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/pkgstore/inner"
)

// Stores through a package qualifier reach the imported global's cell:
// pkg.V = x already worked; compound, nested, and multi-assign forms
// must write through the same base.
func main() {
	inner.V += 1
	inner.V++
	var x int
	inner.V, x = 1, 2
	inner.F.G = 7
	inner.A[0] = 9
	fmt.Println(inner.V, x, inner.F.G, inner.A[0])
}
