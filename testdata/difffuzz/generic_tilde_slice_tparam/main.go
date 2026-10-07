package main

import "fmt"

// `S ~[]E` where E is inferred from S — slices.Sort/Grow's signature.
// The constraint check runs before E is bound.

type Ordered interface {
	~int | ~string
}

func Sort[S ~[]E, E Ordered](x S) int { return len(x) }

func Grow[S ~[]E, E any](s S, n int) S { return append(s, make(S, n)...)[:len(s)] }

func Keys[M ~map[K]V, K comparable, V any](m M) int { return len(m) }

type Names []string

func main() {
	fmt.Println(Sort([]string{"b", "a"}), Sort(Names{"x"}))
	fmt.Println(cap(Grow([]int{1}, 4)) >= 5, Keys(map[string]int{"a": 1}))
}
