package main

import (
	"fmt"
	"unsafe"

	inner "github.com/podhmo/minigo/testdata/difffuzz/sizeof_ucall/inner"
)

func main() {
	fmt.Println(int(unsafe.Sizeof(inner.Val(1, 2))))
}
