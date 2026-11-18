package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_rangeof_call/inner"
)

func onlyInt[T ~int](v T) {
	fmt.Println(v)
}

func main() {
	// the range value's element type is a deferred fact of the call's
	// 0-th result — not the []int container itself.
	for _, v := range inner.Ints() {
		onlyInt(v)
	}
	// same for a receive from a deferred channel-returning call.
	v := <-inner.Ch()
	onlyInt(v)
}
