package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_pkgvarelem/inner"
)

// a package-level var of a deferred type keeps its spelling in the
// marker: resolving it as a member answers the declared element any.
var Items = inner.AnySlice

func useAny[T any](v T) T {
	var z T
	return z
}

func main() {
	for _, v := range Items {
		fmt.Println(useAny(v))
	}
}
