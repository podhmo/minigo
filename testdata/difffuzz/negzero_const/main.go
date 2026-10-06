package main

import (
	"fmt"
	"math"
)

// Untyped floating-point constants have no -0: `-0.0` and tiny
// underflowing magnitudes like -1e-10000 round to +0.
// $GOROOT/test/fixedbugs/issue12577.go (+issue12621.go).

const (
	z0 = 0.0
	z1 = -0.0
	z2 = -z0
)

var m = -1e-10000

func main() {
	fmt.Println(math.Signbit(float64(z1)), math.Signbit(float64(z2)), math.Signbit(m))
	fmt.Println(z1 == 0, z2 == 0, m == 0)
}
