package main

import "fmt"

// Package-level var initialization must follow dependency analysis:
// a var's initializer runs before any var that depends on it, with
// declaration order breaking ties. $GOROOT/test/fixedbugs/issue22326.go.

var (
	_ = d
	_ = f("_", c, b)
	a = f("a")
	b = f("b")
	c = f("c")
	d = f("d")
)

func f(s string, rest ...int) int {
	fmt.Print(s)
	return 0
}

func main() {
	fmt.Println()
}
