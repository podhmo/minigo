package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_rangedeferred/inner"
)

type D int

func main() {
	// i's declared type (int) is a fact: `i == d` must still
	// diagnose the mismatched types when inner is deferred.
	var d D
	for i := range inner.Arr {
		fmt.Println(i == d)
	}
}
