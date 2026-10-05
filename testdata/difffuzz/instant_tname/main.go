package main

import "fmt"

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

func main() {
	fmt.Printf("%T\n", any(Pair[string, int]{"a", 5}))
}
