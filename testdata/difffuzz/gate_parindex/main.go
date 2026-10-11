package main

import "fmt"

type D int

// indexing a pointer to array dereferences it once — `pa[0] == i` is a
// compile error (mismatched types D and int), not a nil-pointer panic.
func main() {
	var pa *[2]D
	var i int
	fmt.Println(pa[0] == i)
}
