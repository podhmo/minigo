package main

import "fmt"

func main() {
	{
		const A = iota
		fmt.Println(A)
	}
	{
		// a sibling block re-binds the function's hidden iota slot —
		// the name binding dies with the first block, the slot does not.
		const B = iota
		const C = iota + 10
		fmt.Println(B, C)
	}
	{
		const (
			D = iota
			E = iota
		)
		fmt.Println(D, E)
	}
}
