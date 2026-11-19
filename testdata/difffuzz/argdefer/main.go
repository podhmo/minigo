package main

import "fmt"

// P4-4b: defer/go calls carry the same CallSite descriptor — arg hints
// resolve eagerly at registration, spread/panic paths behave like OpCall.

func id[T any](v T) T { return v }
func sum[T ~int](xs ...T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	return t
}
func pair[T any, U any](a T, b U) (T, U) { return a, b }

func main() {
	// eval order: callee and args evaluate at registration — the
	// counter reads 1 even though the deferred call runs last.
	n := 0
	bump := func() int { n++; return n }
	defer fmt.Println("deferred saw", id(bump()))
	fmt.Println("body saw", n) // 1

	// spread: a nil slice expands to zero args, and the element
	// typedef still reaches inference (sum's T=int). The deferred
	// generic call itself resolves its hints at registration.
	var xs []int
	defer sum(xs...)
	defer fmt.Println("spread", sum(xs...), sum(1, 2))

	// generic + named receiver method via defer, recovered panic.
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered", r)
		}
	}()
	defer pair(id(7), "x")
	panic("boom")
}
