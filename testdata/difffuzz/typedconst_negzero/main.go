package main

import (
	"fmt"
	"math"
)

// $GOROOT/test/fixedbugs/bug434.go — a typed constant stays a constant
// inside expressions: -float64(zero) folds in the constant domain where
// -0.0 does not exist, while -v on a variable computes a real -0.0.
const zero = 0.0

func main() {
	x := -zero
	if math.Float64bits(x) != 0 {
		fmt.Println("FAIL -zero:", math.Float64bits(x))
		return
	}
	x = -float64(zero)
	if math.Float64bits(x) != 0 {
		fmt.Println("FAIL -float64(zero):", math.Float64bits(x))
		return
	}
	v := x
	if math.Float64bits(-v) != 0x8000000000000000 {
		fmt.Println("FAIL -v:", math.Float64bits(-v))
		return
	}
	// typed constants keep their tag through folding
	fmt.Printf("%v %T\n", int64(5)+1, int64(5)+1)
	fmt.Println("ok")
}
