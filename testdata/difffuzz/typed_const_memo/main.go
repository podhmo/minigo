package main

import "fmt"

// Typed constants and the untyped constants of a variadic call are
// converted once and reused: the cached conversion must stay keyed by
// its target, so one constant read at several types, in several typed
// constant expressions, and packed into several element types keeps
// each result apart.

type M uint8

const (
	k  uint8 = 3
	m  M     = 3
	w  int64 = 7
	lk int64 = 2
)

func ints(xs ...int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func floats(xs ...float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t
}

func bytes(xs ...uint8) uint8 {
	var t uint8
	for _, x := range xs {
		t += x
	}
	return t
}

func anys(xs ...any) string { return fmt.Sprintf("%T %T %v", xs[0], xs[1], xs) }

func main() {
	var s8 uint8
	var sm M
	var s int64
	var q int64
	for i := 0; i < 100; i++ {
		s8 += k
		sm += m
		s += lk * w
		q += w - lk
		q += w / lk
	}
	fmt.Println(s8, sm, s, q)                  // 44 44 1400 800
	fmt.Printf("%T %T %T\n", s8+k, sm+m, lk*w) // uint8 main.M int64
	a, b, c := 0, 0.0, uint8(0)
	for i := 0; i < 3; i++ {
		a += ints(i, 2, 3)
		b += floats(float64(i), 2, 3) / 2
		c += bytes(250, 3, uint8(i))
	}
	fmt.Println(a, b, c, anys(2, 'a'), anys(2.5, k)) // 18 9 250 int int32 [2 97] float64 uint8 [2.5 3]
	fmt.Println(lk*w == 14, w/lk, w%lk, w-lk)        // true 3 1 5
}
