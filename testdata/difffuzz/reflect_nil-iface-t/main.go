package main

import "fmt"

func main() {
	var a any
	fmt.Printf("%T\n", a)
	fmt.Printf("%v %v\n", a, a == nil)
	var e error
	fmt.Printf("%T %v\n", e, e)
}
