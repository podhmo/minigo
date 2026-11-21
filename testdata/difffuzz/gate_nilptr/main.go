package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_nilptr/inner"
)

func main() {
	x := inner.P()
	fmt.Println(x == nil)
}
