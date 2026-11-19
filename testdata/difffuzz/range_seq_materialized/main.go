package main

import (
	"fmt"
	"slices"
	"strings"
)

// A seq whose elements exist up front (strings.SplitSeq, slices.Values,
// seqOf-built producers) ranges through the materialized slice — order,
// break/continue, nested loops and the element-binding all match the
// producer-driven semantics.

func main() {
	i := 0
	for s := range strings.SplitSeq("a,b,c", ",") {
		fmt.Println(i, s)
		i++
	}
	sum := 0
	for v := range slices.Values([]int{10, 20, 30}) {
		if v == 20 {
			continue
		}
		sum += v
	}
	fmt.Println(sum)
	for v := range slices.Values([]int{1, 2, 3}) {
		if v == 2 {
			break
		}
		fmt.Println("v", v)
	}
	// nested materialized seqs
	for a := range slices.Values([]string{"x", "y"}) {
		for b := range slices.Values([]int{1, 2}) {
			fmt.Print(a, b, " ")
		}
	}
	fmt.Println()
	// a Seq2 producer still goes through yield (maps.All) — covered
	// separately; here a seq passed as a func value also iterates.
	var seq func(func(int) bool) = slices.Values([]int{7, 8})
	for v := range seq {
		fmt.Println(v)
	}
}
