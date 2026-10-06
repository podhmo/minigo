package main

import "fmt"

type E error

func main() {
	type K struct{ X any }
	m := map[K]int{K{nil}: 7}
	var e error
	var e2 E
	var a any
	var p *int
	// a boxed typed nil keeps its dynamic type: (*int)(nil) is a
	// different key from a nil interface.
	m[K{p}] = 3
	fmt.Println(m[K{e}], m[K{e2}], m[K{a}], m[K{p}])
	fmt.Println(K{nil} == K{e}, K{nil} == K{e2}, K{nil} == K{p})

	type A struct{ X [2]any }
	ma := map[A]int{A{[2]any{nil, 1}}: 5}
	fmt.Println(ma[A{[2]any{e, 1}}], ma[A{[2]any{p, 1}}])
}
