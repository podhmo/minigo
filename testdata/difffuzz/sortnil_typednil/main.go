package main

import (
	"fmt"
	"sort"
)

// A nil slice is a valid sort input in Go: sorting it is a no-op and it
// counts as sorted. Typed nils of other kinds (pointers, maps, chans)
// are still rejected — the minigo side traps on them.

type List []int

func main() {
	var s []int
	sort.Slice(s, func(i, j int) bool { return false })
	var l List
	sort.Slice(l, func(i, j int) bool { return false })
	sort.SliceStable(s, func(i, j int) bool { return false })
	fmt.Println(sort.SliceIsSorted(s, func(i, j int) bool { return false }))
	sort.Ints(s)
	sort.Float64s([]float64(nil))
	sort.Strings([]string(nil))
	fmt.Println("ok")
}
