package main

import "fmt"

type A any
type N int

func main() {
	var nv N = 1
	fmt.Println(A(nv) == 1, any(nv) == 1, A(nv) == nv)
	fmt.Println(A(N(1)) == 1)
}
