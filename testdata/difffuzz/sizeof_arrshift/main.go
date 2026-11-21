package main

import (
	"fmt"
	"unsafe"

	inner "github.com/podhmo/minigo/testdata/difffuzz/sizeof_arrshift/inner"
)

func main() {
	fmt.Println(unsafe.Sizeof(inner.G()), unsafe.Sizeof(inner.H()))
}
