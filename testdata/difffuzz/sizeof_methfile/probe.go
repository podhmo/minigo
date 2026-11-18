package main

import (
	"fmt"
	"unsafe"

	q "github.com/podhmo/minigo/testdata/difffuzz/sizeof_methfile/dep3"
)

func init() {
	fmt.Println(int(unsafe.Sizeof(r)), int(unsafe.Sizeof(q.T{})))
}
