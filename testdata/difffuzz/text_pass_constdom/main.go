package main

import (
	"fmt"
	"math"
)

const (
	c1e3   = 1e3
	chuge  = 1 << 100
	cshift = chuge >> 100
	bigcmp = 1e100 - 1
)

func main() {
	// untyped consts stay in the constant domain through decl storage so
	// each use materializes for its context ($GOROOT/test/{intcvt,const,shift3}.go).
	var i int = c1e3
	var f float64 = c1e3
	fmt.Println(i, f)

	// the const converts to the operand's type before comparing.
	var g float64 = 1e100 - 1
	fmt.Println(g == bigcmp)

	// a non-const shift reads a UConst count as uint — 2^64-1 clamps to 0.
	var x int64 = 1
	fmt.Println(x << (math.MaxUint + 0))

	// const-domain shifts fold exactly.
	fmt.Println(cshift)
	fmt.Println(x << 1.)
	fmt.Println(x << (1 + 0i))
}
