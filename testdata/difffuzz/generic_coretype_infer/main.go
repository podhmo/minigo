package main

// constraint type inference: Map ~map[K]V teaches K and V, S ~[]E
// teaches E — maps.Values / slices.Collect under oapi-codegen.

import (
	"fmt"
	"iter"
)

func Values[Map ~map[K]V, K comparable, V any](m Map) iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, v := range m {
			if !yield(v) {
				return
			}
		}
	}
}

func First[S ~[]E, E any](s S) E {
	var z E
	if len(s) > 0 {
		return s[0]
	}
	return z
}

type M map[string]int
type Names []string

func main() {
	n := 0
	for v := range Values(M{"a": 1, "b": 2}) {
		n += v
	}
	fmt.Println(n, First(Names{"x"}), First([]int(nil)))
	fmt.Printf("%T\n", First([]*M{}))
}
