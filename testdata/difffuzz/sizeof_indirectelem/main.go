package main

import (
	"fmt"
	"unsafe"

	dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_indirectelem/dep2"
)

func main() {
	s := dep.Get()
	fmt.Println(unsafe.Sizeof(s), unsafe.Sizeof(s[0]), unsafe.Alignof(s))
}
