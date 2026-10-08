package main

import "fmt"

type A struct{ X int }
type B struct{ X int }

func main() {
	// gc rejects at compile time: B{1} is not assignable to map[A]'s key
	// type. minigo accepts structurally-identical named structs.
	m := map[A]int{}
	m[A{1}] = 1
	m[B{1}] = 2
	fmt.Println(len(m))
}
