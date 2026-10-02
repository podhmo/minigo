package main

import "fmt"

func before() int {
	fmt.Println("rhs evaluated")
	return 1
}

type T struct{ f int }

func main() {
	sliceStore()
	fieldStore()
}

func sliceStore() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered")
		}
	}()
	var s []int
	// Go evaluates the RHS before the bounds check on the store
	// target fires — "rhs evaluated" prints, then the panic.
	s[0] = before()
}

func fieldStore() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered")
		}
	}()
	var p *T
	p.f = before()
}
