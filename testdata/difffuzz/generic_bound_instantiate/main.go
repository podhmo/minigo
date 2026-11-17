package main

import (
	"fmt"
	"iter"
	"maps"
	"slices"
)

func main() {
	// explicit instantiation of bound generic funcs
	f := slices.Contains[[]int]
	fmt.Println(f([]int{1}, 1), f([]int{2}, 1))
	fmt.Println(slices.MaxFunc([][]int{{1, 2}, {3}}, slices.Compare[[]int]))
	fmt.Println(slices.Equal[[]int]([]int{1}, []int{1}), slices.IsSortedFunc([][]string{{"a"}, {"b"}}, slices.Compare[[]string]))

	// the instantiated value passes as a callback like any func
	var cmpFn func(a, b []int) int = slices.Compare[[]int]
	fmt.Println(cmpFn([]int{1}, []int{2}))

	// maps generics instantiate too
	for k := range maps.Keys[map[string]int](map[string]int{"x": 1}) {
		fmt.Println(k)
	}
	fmt.Println(maps.Equal[map[string]int](map[string]int{"a": 1}, map[string]int{"a": 1}))
	named()
}

type S []int

func named() {
	// the type argument types nils and results
	fmt.Printf("%T %v\n", slices.Clone[[]int](nil), slices.Clone[[]int](nil))
	fmt.Printf("%T\n", slices.Clone[S]([]int{1}))
	fmt.Printf("%T %v\n", slices.Concat[[]int](), slices.Concat[[]int]())
	fmt.Printf("%T %v\n", maps.Clone[map[string]int](nil), maps.Clone[map[string]int](nil))

	// Collect[E] returns []E, not E: a named-slice element arg does
	// not retag the result.
	fmt.Printf("%T\n", slices.Collect[S](seq()))

	// a nil binds its declared param's targ, not the positional one:
	// BinarySearchFunc[S,E,T]'s target is T.
	i, ok := slices.BinarySearchFunc[[]*int, *int, *string]([]*int{new(int)}, nil,
		func(e *int, t *string) int { return 0 })
	fmt.Println(i, ok)

	// an interface targ's nil stays the bare nil the elements hold.
	fmt.Println(slices.Contains[[]any, any]([]any{nil}, nil))

	// a nil binds positionally even under a full explicit
	// instantiation — Clone[S,E]'s first arg is S, not E.
	fmt.Printf("%T\n", slices.Clone[[]int, int](nil))
}

func seq() iter.Seq[S] {
	return func(yield func(S) bool) { yield(S{1}); yield(S{2}) }
}
