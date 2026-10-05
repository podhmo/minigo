package main

import "fmt"

// gc evaluates the call in the right operand before reading the plain left
// operand, so inc() runs first and c reads 3: 3 + 3 = 6.
func f() int {
	c := 0
	inc := func() int { c++; return c }
	inc()
	inc()
	return c + inc()
}

func main() {
	fmt.Println(f())
}
