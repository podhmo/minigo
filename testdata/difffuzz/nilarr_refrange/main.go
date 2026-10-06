package main

import "fmt"

// `for i, _ := range arr` over a nil *[N]T iterates without
// dereferencing the pointer (the `_` element is never read).
// $GOROOT/test/fixedbugs/bug454.go.

func main() {
	var arr *[10]int
	s := 0
	for i, _ := range arr {
		s += i
	}
	fmt.Println(s)
}
