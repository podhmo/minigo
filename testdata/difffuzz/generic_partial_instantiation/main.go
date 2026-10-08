package main

import "fmt"

func Grow[S ~[]E, E any](s S, n int) S {
	if n -= cap(s) - len(s); n > 0 {
		s = append(s[:cap(s)], make([]E, n)...)[:len(s)]
	}
	return s
}

func Pair[K comparable, V any](k K) map[K]V { return map[K]V{} }

type Names []string

func Conv[To, From any](f From) To {
	var t To
	fmt.Printf("%T->%T\n", f, t)
	return t
}

func main() {
	s := Grow[Names](nil, 3)
	fmt.Printf("%T %d %v\n", s, len(s), cap(s) >= 3)
	f := Grow[[]int]
	fmt.Println(len(f([]int{1}, 2)))
	m := Pair[string, int]("a")
	fmt.Printf("%T\n", m)
	_ = Conv[string](3)
	_ = Conv[int, float64](1.5)
}
