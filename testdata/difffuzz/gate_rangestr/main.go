package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_rangestr/inner"
)

type D2 rune

func main() {
	var d D2
	for _, v := range inner.S {
		fmt.Println(v == d)
	}
}
