package main

import (
	"fmt"
	"unsafe"

	inner "github.com/podhmo/minigo/testdata/difffuzz/sizeof_arrconst/inner"
)

func main() {
	fmt.Println(unsafe.Sizeof(inner.G()))
}
