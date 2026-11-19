package main

import (
	"fmt"
	"slices"
)

func gen(yield func(int) bool) {
	xs := []int{1, 2, 3, 4, 5}
	found := slices.ContainsFunc(xs, func(x int) bool {
		return !yield(x)
	})
	fmt.Println("found:", found)
}

func deep(n int) int {
	if n <= 0 {
		return 0
	}
	return deep(n-1) + 1
}

func main() {
	for v := range gen {
		fmt.Println("body saw", v, deep(200))
	}
}
