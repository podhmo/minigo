package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/convarg_seqorder/inner"
)

func main() {
	var x = 1
	// Bump is a call: it must hoist, so x reads after it (normal-mode
	// two-phase order prints 2 2).
	fmt.Println(x, inner.Bump(&x))
}
