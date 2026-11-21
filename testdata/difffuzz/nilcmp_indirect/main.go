package main

import (
	"fmt"

	dep "github.com/podhmo/minigo/testdata/difffuzz/nilcmp_indirect/dep2"
)

func main() {
	s := dep.Get()
	fmt.Println(s == nil)
}
