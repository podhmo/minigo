package main

import (
	"fmt"
	"unsafe"

	inner "github.com/podhmo/minigo/testdata/difffuzz/sizeof_pkgslice/inner"
)

var xs = inner.Bytes{1, 2, 3}

var n int

func next() int { n++; return 0 }

func main() {
	inner := 0 // local shadowing the import name
	fmt.Println(int(unsafe.Sizeof(xs[next()])), inner, n)
}
