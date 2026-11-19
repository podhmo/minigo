package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_chanpkgmismatch/origin"
)

func main() {
	// the element type of the deferred channel member is origin.Count;
	// the mismatch gate must keep its package name.
	origin.Ch <- 0
	var s string
	select {
	case v := <-origin.Ch:
		var zero origin.Count
		if v != zero {
			panic("unexpected receive value")
		}
		fmt.Println(v == s)
	default:
	}
}
