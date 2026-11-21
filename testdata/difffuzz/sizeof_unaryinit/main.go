package main

import (
	"fmt"
	"unsafe"

	dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_unaryinit/dep2"
)

var _ dep.Other // dep resolves to dep2 here, which has no Word

func main() {
	fmt.Println(int(unsafe.Sizeof(V)))
}
