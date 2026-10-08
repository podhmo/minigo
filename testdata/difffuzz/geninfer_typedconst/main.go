package main

import "fmt"

func p[T any](x, y T) T { return x }

func main() {
	const k int8 = 7
	fmt.Println(p(int8(3), 'a') + 0)
	fmt.Println(p('a', int8(3)) + 0)
	fmt.Println(p(k, 'a') + 0)
}
