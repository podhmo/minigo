package main

import (
	"fmt"
	"unsafe"

	inner "github.com/podhmo/minigo/testdata/difffuzz/sizeof_namedelem/inner"
)

var xs [4]inner.T

var n int

func next() int { n++; return 0 }

func main() {
	inner := 0 // local shadowing the import name
	fmt.Println(int(unsafe.Sizeof(xs[next()])), inner, n)
}
