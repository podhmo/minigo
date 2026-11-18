package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_rangenested/inner"
)

func onlyInt[T ~int](v T) {
	fmt.Println(v)
}

func main() {
	// the outer element feeds the inner range: row's deferred marker
	// is itself an element marker (nil Index), not a result index.
	for _, row := range inner.Grid() {
		for _, v := range row {
			onlyInt(v)
		}
	}
}
