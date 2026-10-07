package main

import "fmt"

// An untyped-constant operand in a binary op converts to the OTHER
// operand's type when representable — `k * 2e6` computes in int
// (2e6 is exactly representable), not in float64. minigo materialized
// the constant by its syntactic kind first, so int math on float-kind
// constants printed `6e+06` and assigning the result into an int
// element trapped "cannot use float64 as int" (corpus issue20780b).

const N = 2e6 // untyped float-notation constant, exactly an int

func main() {
	k := 3
	fmt.Println(k * N)   // int arithmetic: 6000000, not 6e+06
	fmt.Println(k * 2e6) // literal form too
	var x [4]int
	x[0] = k*N + 0 // store into an int element
	fmt.Println(x[0])
	f := 1.5
	fmt.Println(f * N)   // float side still gives float
	fmt.Println(k - 1e2) // 1e2 representable: int subtraction
	_ = N
}
