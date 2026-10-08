package main

import "fmt"

type KeyValue struct{ K, V int }

// SortedMap is a named slice, like internal/fmtsort's.
type SortedMap []KeyValue

func inner[E any](data []E, n int, less func(a, b E) bool) {
	for i := 1; i < n; i++ {
		for j := i; j > 0 && less(data[j], data[j-1]); j-- {
			data[j], data[j-1] = data[j-1], data[j]
		}
	}
}

// outer mirrors slices.SortStableFunc: S ~[]E passed on as []E.
func outer[S ~[]E, E any](x S, less func(a, b E) bool) {
	inner(x, len(x), less)
}

func main() {
	m := SortedMap{{3, 30}, {1, 10}, {2, 20}}
	outer(m, func(a, b KeyValue) bool { return a.K < b.K })
	fmt.Println(m)
}
