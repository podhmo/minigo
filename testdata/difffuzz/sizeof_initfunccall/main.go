package main

import (
	"fmt"
	"unsafe"

	dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_initfunccall/dep2"
)

var _ dep.F // keep dep2 a used import

func main() {
	fmt.Println(int(unsafe.Sizeof(V)))
}
