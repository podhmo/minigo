package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/const_rune_dte/inner"
)

func f(r rune) rune { return r + 1 }

func main() {
	var y = inner.C
	fmt.Printf("%T\n", y)
	fmt.Printf("%c\n", f(y))
}
