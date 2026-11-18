package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_nilinit/inner"
)

// V's initializer runs a deferred gate inside this package's own init:
// resolving the `nil` operand through the member table would re-enter
// that very initializer.
var V = inner.P == nil

func main() {
	fmt.Println(V)
}
