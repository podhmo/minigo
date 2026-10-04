package main

import "fmt"

type J interface {
	Method()
}

type C128 complex128

func (C128) Method() {}

func main() {
	var c128 C128
	var jc128 J = c128
	jc128.Method()
	fmt.Println(c128 == 0)
}
