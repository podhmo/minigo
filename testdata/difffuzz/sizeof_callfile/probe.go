package main

import (
	"fmt"
	"unsafe"

	dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_callfile/dep2"
)

func Probe() int {
	// in this file `dep` names dep2 — s's type must still be dep1's
	return int(unsafe.Sizeof(s)) + int(unsafe.Sizeof(dep.Other{}.A))
}

func init() {
	fmt.Println(Probe())
}
