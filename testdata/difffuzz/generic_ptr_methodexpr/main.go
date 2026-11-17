package main

import "fmt"

type G[T any] struct{ xs []T }

func (g *G[T]) Push(xs ...T) { g.xs = append(g.xs, xs...) }

func main() {
	g := &G[string]{}
	pe := (*G[string]).Push
	pe(g, "a", "b")
	fmt.Println(g.xs)

	n := &G[int]{}
	pn := (*G[int]).Push
	pn(n, 1, 2, 3)
	fmt.Println(n.xs)
}
