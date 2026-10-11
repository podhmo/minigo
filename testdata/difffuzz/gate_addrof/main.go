package main

import "fmt"

type D int

// &x answers a pointer to x's static type — `i == &d` is a compile
// error (mismatched types int and *D), not a silent false.
func main() {
	var d D
	var i int
	fmt.Println(i == &d)
}
