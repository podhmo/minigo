package main

import (
	"fmt"
	"unsafe"

	inner "github.com/podhmo/minigo/testdata/difffuzz/sizeof_noeval/inner"
)

func boom() int { panic("boom ran") }

func main() {
	fmt.Println(int(unsafe.Sizeof(inner.Panicky(boom()))))
}
