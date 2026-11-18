package main

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_binddeferred/inner"
)

type D int

func main() {
	// b's declared type is Pair's second result — a deferred fact.
	a, b := inner.Pair()
	var d D
	fmt.Println(a == d, b == d)
}
