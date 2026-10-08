package main

import "fmt"

func p[T any](x, y T) T { return x }

func main() {
	fmt.Printf("%T\n", p(5, 2.0))
	fmt.Printf("%T\n", p(5, 'a'))
	fmt.Printf("%T\n", p('a', 5))
}
