package main

import "fmt"

// `&&`, `||` and `!` on a named bool keep its type, whichever operand
// decides the result: `v && true` with `type B bool` is a B even when
// the untyped true comes out, so it compares with other B values.

type B bool

func main() {
	var v B = true
	x := v && true
	fmt.Printf("%T %v\n", x, x) // main.B true
	var w B = v && v
	fmt.Println(w == x) // true
	y := false || v
	z := v || 1 > 2
	fmt.Printf("%T %T %v %v\n", y, z, y, z) // main.B main.B true true
	u := !v && true
	fmt.Printf("%T %v\n", u, u) // main.B false
	var p bool = true
	q := p && true
	fmt.Printf("%T\n", q) // bool
}
