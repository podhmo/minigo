package main

import (
	"fmt"
	"iter"
	"slices"
)

func main() {
	next, stop := iter.Pull(slices.Values([]int{7, 8}))
	defer stop()
	for {
		v, ok := next()
		fmt.Println(v, ok)
		if !ok {
			break
		}
	}

	next2, stop2 := iter.Pull2(slices.All([]string{"a", "b"}))
	defer stop2()
	for {
		k, v, ok := next2()
		fmt.Println(k, v, ok)
		if !ok {
			break
		}
	}

	// early stop: the worker's coroexit must release the stopper
	next3, stop3 := iter.Pull(slices.Values([]int{1, 2, 3, 4, 5}))
	v, ok := next3()
	fmt.Println(v, ok)
	stop3()
	v, ok = next3()
	fmt.Println(v, ok)
	stop3()

	// a panic inside the seq propagates to the pulling next()
	func() {
		defer func() { fmt.Println("recovered:", recover() != nil) }()
		n4, s4 := iter.Pull(iter.Seq[int](func(yield func(int) bool) {
			yield(1)
			panic("boom")
		}))
		defer s4()
		fmt.Println(n4())
		fmt.Println(n4())
	}()

	// a script seq value Pull drives through the coroutine
	n5, s5 := iter.Pull(iter.Seq[int](func(yield func(int) bool) {
		for _, x := range []int{9, 9} {
			if !yield(x) {
				return
			}
		}
	}))
	defer s5()
	fmt.Println(n5())
	fmt.Println(n5())
	fmt.Println(n5())
}
