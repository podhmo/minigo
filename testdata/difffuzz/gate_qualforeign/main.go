package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualforeign/inner"
)

// `var y = inner.Dur` binds y as time.Duration — gc rejects `y == i`
// (mismatched types time.Duration and int64). The caller does not
// import time: the operand type arrives through a synthesized
// qualifier binding inner's own import ref.
func main() {
	var y = inner.Dur
	var i int64
	fmt.Println(y == i)
}
