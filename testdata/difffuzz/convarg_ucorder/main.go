package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/convarg_ucorder/inner"
)

func main() {
	// Bump runs before read: Min(1, 1) prints 1.
	x := 0
	read := func() int { return x }
	fmt.Println(inner.Min(float64(inner.Bump(&x)), read()))
}
