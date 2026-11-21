package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_rangelocal/inner"
)

// items shares its name with a package-level var of a different
// element type — the deferred bind marker must resolve the local
// chain, not the package member.
var items = []string{"a"}

func onlyInt[T ~int](v T) {
	fmt.Println(v)
}

func main() {
	items := inner.Ints
	for _, v := range items {
		onlyInt(v)
	}
}
