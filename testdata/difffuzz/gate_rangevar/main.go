package main

import "fmt"

type D int

// `for i := range a` binds int — comparing it against a named type is
// a compile error (mismatched types int and D), not a silent false.
func main() {
	var d D
	for i := range [2]int{1, 2} {
		fmt.Println(i == d)
	}
}
