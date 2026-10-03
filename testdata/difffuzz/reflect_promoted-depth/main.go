package main

import "fmt"

type A struct{ X int }
type D struct{ A }
type B struct{ X int }
type S struct {
	D
	B
}

func main() { s := S{D: D{A: A{X: 1}}, B: B{X: 2}}; fmt.Println(*(&s.X), s.X) }
