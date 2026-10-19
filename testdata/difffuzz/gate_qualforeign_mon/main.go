package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualforeign_mon/inner"
)

// `var m = inner.Mon` binds m as time.Month — gc rejects `m == i`
// (mismatched types time.Month and int).
func main() {
	var m = inner.Mon
	var i int
	fmt.Println(m == i)
}
