package main

import "fmt"

// Typed const decls keep their declared type and their constness:
// arithmetic stays in that type, a var copy wraps at run time, and a
// typed const shift count keeps an untyped left operand constant.

type M uint8

const (
	c int8    = 100
	N int     = 5
	F float64 = 7
	m M       = 200
	T uint    = 3
)

const (
	A uint8 = iota + 254
	B
)

func main() {
	fmt.Println(N/2.0, F/2, int64(5)/2.0) // 2 3.5 2
	fmt.Printf("%T %T %T %T %v %T %v\n", N, F, c, m, m, B, B)
	const d int8 = 100
	const e uint = 3
	fmt.Println((1<<e)/16.0, (1<<T)/16.0, d, c) // 0.5 0.5 100 100
	x := c
	fmt.Println(x+x, d-c, m+50, A+1) // -56 0 250 255
	fmt.Printf("%T %T %T\n", x, m+1, T+0)
}
