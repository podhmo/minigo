package main

import "fmt"

// constraint type inference through a ~*E core type: PE's arg (*int)
// teaches E=int.
func zero[E any, PE interface{ ~*E }](p PE) E { return *p }

type N int

func zeroN[E any, PE interface{ ~*E }](p PE) E { return *p }

func main() {
	x := 5
	fmt.Println(zero(&x))
	var n N = 7
	fmt.Println(zeroN(&n))
}
