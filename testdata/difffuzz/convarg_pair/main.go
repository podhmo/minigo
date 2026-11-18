package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/convarg_pair/inner"
)

func main() {
	fmt.Println(inner.Sum(inner.Pair()))
}
