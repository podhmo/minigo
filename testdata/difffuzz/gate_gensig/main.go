package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_gensig/inner"
)

var s = "x"

func main() {
	fmt.Println(inner.F(s) == s)
}
