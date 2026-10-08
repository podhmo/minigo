package main

import (
	"fmt"
	"strconv"
)

type Celsius float64
type IDs []int

func id[T any](x T) T { return x }

func kind[T any](x T) string { return fmt.Sprintf("%T", x) }

func join[T any](a, b T) string { return fmt.Sprintf("%T %v %v", a, a, b) }

func mapf[E, R any](xs []E, f func(E) R) []R {
	out := make([]R, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func first[S ~[]E, E any](s S) (E, string) {
	var zero E
	if len(s) == 0 {
		return zero, fmt.Sprintf("%T", s)
	}
	return s[0], fmt.Sprintf("%T", s)
}

func sum[T int | float64](xs ...T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	return t
}

type Box[T any] struct{ V T }

func (b Box[T]) With(v T) Box[T] { return Box[T]{v} }

func main() {
	// one call site, many argument types: each call infers anew
	vals := []any{1, "s", 2.5, Celsius(3), IDs{1}, []string{"x"}, nil}
	for _, v := range vals {
		fmt.Println(kind(v), kind(id(v)))
	}
	for i := 0; i < 2; i++ {
		fmt.Println(kind(1), kind(2.5), kind('a'), kind("s"), kind(Celsius(1)), kind(int8(2)))
		fmt.Println(join(1, 2), join(1, 2.5), join('a', 2), join('a', 2.5), join(Celsius(1), 2))
	}
	fmt.Println(mapf([]int{1, 2}, strconv.Itoa), mapf([]int{1, 2}, func(i int) float64 { return float64(i) / 2 }))
	fmt.Println(mapf([]string{"a"}, func(s string) int { return len(s) }))
	fmt.Println(first(IDs{4}))
	fmt.Println(first([]int{5}))
	fmt.Println(first([]Celsius{}))
	fmt.Println(sum(1, 2), sum(1.5, 2), sum[int](), sum([]float64{1, 2}...), sum([]int{3}...))
	b := Box[int]{1}
	fmt.Println(b.With(2), Box[string]{"a"}.With("b"))
}
