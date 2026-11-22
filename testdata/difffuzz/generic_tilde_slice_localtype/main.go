package main

import "fmt"

// `S ~[]E` against a func-local element type: the argument's underlying
// view must keep the element's declared identity — like the bound E —
// or the constraint check rejects `[]L` (#1350).

func F[T ~[]E, E any](v T) T { return append(v, v[0]) }

func a() {
	type L int
	fmt.Println(F([]L{1}))
}

func b() {
	type L string
	fmt.Println(F([]L{"a"}))
}

func main() { a(); b() }
