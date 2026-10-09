package main

import (
	"cmp"
	"fmt"
	"slices"
)

type Name string

type Pair[T any] struct{ A, B T }

func show[T any](x T) string {
	var zero T
	s := []T{x, zero}
	p := Pair[T]{x, zero}
	return fmt.Sprintf("%T %v %T %v %T", x, s, s, p, p)
}

func outer[T any](x T) string {
	type local struct{ V T }
	return fmt.Sprintf("%T %v", local{x}, local{x})
}

func max2[T cmp.Ordered](a, b T) T {
	if cmp.Less(a, b) {
		return b
	}
	return a
}

func main() {
	// the same instantiation from many call sites and fresh inferences
	for i := 0; i < 3; i++ {
		fmt.Println(show("s"), show(i), show(Name("n")), show([]byte("b")))
	}
	fmt.Println(show(1.5), show(int8(3)), show[any](nil), show(Pair[int]{1, 2}))
	fmt.Println(outer(1), outer("x"), outer(1))
	fmt.Println(max2(1, 2), max2("a", "b"), max2(Name("x"), Name("y")), max2(1.5, 0.5))
	xs := []string{"b", "a", "c"}
	slices.Sort(xs)
	ns := []Name{"z", "y"}
	slices.Sort(ns)
	is := []int{3, 1, 2}
	slices.Sort(is)
	fmt.Println(xs, ns, is, cmp.Compare("a", "b"), cmp.Compare(2, 1), cmp.Compare(Name("a"), Name("a")))
}
