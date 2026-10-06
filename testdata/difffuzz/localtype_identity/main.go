package main

import "fmt"

func one() any { type T int; return T(0) }
func two() any { type T int; return T(0) }

func main() {
	// p and q have different dynamic types; this comparison must be false.
	fmt.Println(one() == two())
}
