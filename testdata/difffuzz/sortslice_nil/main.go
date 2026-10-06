package main

import (
	"fmt"
	"sort"
)

func main() {
	var xs []int
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	fmt.Println("ok", len(xs))
}
