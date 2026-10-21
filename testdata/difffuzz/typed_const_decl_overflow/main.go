package main

import "fmt"

// A typed const decl `const c int8 = 100` is the constant expression
// int8(100): `c + c` is a constant overflowing int8, gc's compile error
// ("c + c (constant 200 of type int8) overflows int8") — not the -56 a
// var would wrap to.

const c int8 = 100

func main() {
	fmt.Println(c + c)
}
