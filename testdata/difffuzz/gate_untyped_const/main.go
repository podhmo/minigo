package main

import "fmt"

type M int

const c = 5

// y := <untyped const ident> binds the const's default type — `y == m`
// is a compile error (mismatched types int and M), not false.
func main() {
	var m M
	y := c
	fmt.Println(y == m)
}
