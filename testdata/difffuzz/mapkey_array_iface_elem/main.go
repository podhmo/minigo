package main

import "fmt"

// Array keys with interface-typed elements hash by the element's
// dynamic type: [1]any{M(2)} and [1]any{uint32(2)} are different keys.

type M int

func main() {
	m := map[[1]any]int{{M(2)}: 1}
	fmt.Println(m[[1]any{uint32(2)}]) // 0: different dynamic type
	fmt.Println(m[[1]any{M(2)}])      // 1
	fmt.Println(m[[1]any{2}])         // 0: int, not M
	fmt.Println(m[[1]any{nil}])       // 0: nil element

	// same representation question on a non-interface element type:
	// a converted and a direct element hit the same key.
	n := map[[1]M]int{{M(2)}: 3}
	var two M = 2
	fmt.Println(n[[1]M{two}]) // 3

	// a string spelled like a type tag is a different key than the
	// tagged value: [1]any{"uint32:2"} vs [1]any{uint32(2)}.
	s := map[[1]any]int{{"uint32:2"}: 5, {"nil"}: 6}
	fmt.Println(s[[1]any{uint32(2)}])  // 0
	fmt.Println(s[[1]any{"uint32:2"}]) // 5
	fmt.Println(s[[1]any{nil}])        // 0
}
