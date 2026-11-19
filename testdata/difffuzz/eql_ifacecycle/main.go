package main

import "fmt"

type A any
type N int

func main() {
	// conversions to a defined-over-interface type keep the
	// payload's dynamic type: A(N(1)) holds N, not int.
	fmt.Println(A(N(1)) == 1, any(N(1)) == 1, N(1) == 1, A(1) == 1)
	var n N = 2
	var a2 any = n
	fmt.Println(a2 == N(2), a2 == 2)
	// a self-referential interface value compares without
	// recursing into its ref cycle.
	var a any
	p := &a
	a = p
	fmt.Println(a == a)
}
