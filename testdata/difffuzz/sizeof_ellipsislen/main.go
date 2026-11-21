package main

import (
	"fmt"
	"unsafe"

	dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_ellipsislen/dep"
)

func main() {
	fmt.Println(unsafe.Sizeof(dep.V), unsafe.Sizeof(dep.V[0]), len(dep.V))
}
