package main

import "fmt"

// shadowing the builtin error name: `type Code error` is a declared
// type over int, not over an interface — Code(1) keys apart from int.
type error = int
type Code error

func main() {
	m := map[any]int{1: 1}
	m[Code(1)] = 2
	fmt.Println(m[1], m[Code(1)], len(m))
}
