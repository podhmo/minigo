package main

import (
	"fmt"
	"unsafe"

	dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_promoted/dep2"
)

var _ = dep.T(0) // keep dep2 a used import

type Outer struct{ Inner }

var o Outer

func main() {
	// the promoted field's type resolves in Inner's file, where
	// `dep` is dep1 (T = 16) — not here, where dep is dep2 (T = 1).
	fmt.Println(int(unsafe.Sizeof(o.Arr)))
}
