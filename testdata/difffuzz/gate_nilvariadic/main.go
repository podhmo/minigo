package main

import "fmt"

// A variadic parameter's type spells `...T` — an Ellipsis the gate
// cannot name but must still read as the nilable slice it is.
func f(xs ...int) bool { return xs == nil }

func main() {
	fmt.Println(f(), f(1))
}
