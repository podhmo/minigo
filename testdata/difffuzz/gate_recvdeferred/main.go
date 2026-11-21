package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_recvdeferred/inner"
)

func main() {
	// v's element type (int) is a fact of the deferred channel.
	inner.Ch <- 0
	var s string
	select {
	case v := <-inner.Ch:
		var zero inner.Int
		if v != zero {
			panic("unexpected receive value")
		}
		fmt.Println(v == s)
	default:
	}
}
