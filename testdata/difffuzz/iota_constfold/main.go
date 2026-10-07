package main

import "fmt"

// $GOROOT/test/fixedbugs/issue6866.go — iota is a constant: `1.0/(iota+N)`
// divides in the exact-constant domain, not float64. minigo stored the
// hidden iota local as int64, so a float-constant expression using iota
// materialized to float64 and the last mantissa bit rounded differently.
const (
	h0_0 = 1.0 / (iota + 1)
	h0_1 = 1.0 / (iota + 2)
	h1_0 = 1.0 / (iota + 3)
	h1_1 = 1.0 / (iota + 4)
)

func f() {
	const (
		l0 = 1.0 / (iota + 1)
		l1 = 1.0 / (iota + 2)
	)
	fmt.Println(l0*4 + l1*-12)
}

func main() {
	fmt.Println(h0_0*4 + h0_1*-12 + h1_0*-12 + h1_1*36)
	fmt.Println(h1_0 * -12)
	f()
}
