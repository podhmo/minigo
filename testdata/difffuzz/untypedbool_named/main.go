package main

import "fmt"

// a constant predicate is an untyped bool — it adopts into a named
// operand like any untyped literal.
type Flag bool

var f Flag = true

func main() {
	fmt.Println(f && (1 == 1))
	fmt.Println(f == (2 > 1))
	fmt.Println(f == !false)
}
