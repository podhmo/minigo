package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/shift_lenarr/inner"
)

const H = 1 << len(inner.A)

func main() {
	fmt.Println(H >> 100)
}
