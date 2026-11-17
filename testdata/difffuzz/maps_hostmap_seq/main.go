package main

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"sort"
)

func main() {
	h := map[string]int{"b": 2, "a": 1, "c": 3}
	fmt.Printf("%T\n", maps.Keys(h))
	fmt.Printf("%T\n", maps.All(h))
	fmt.Println(slices.Sorted(maps.Keys(h)))
	fmt.Println(slices.Sorted(maps.Values(h)))
	type pair struct {
		k string
		v int
	}
	var pairs []pair
	for k, v := range maps.All(h) {
		pairs = append(pairs, pair{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	for _, p := range pairs {
		fmt.Println(p.k, p.v)
	}
	dst := map[string]int{}
	maps.Insert(dst, maps.All(h))
	fmt.Println(dst["a"], dst["b"], dst["c"], len(dst))
	// laziness: empty the map before consuming the seq — yields nothing
	lazy := maps.Keys(h)
	for k := range h {
		delete(h, k)
	}
	fmt.Println(slices.Collect(lazy), len(h))
	// nil host map yields nothing
	var nm map[string]int
	fmt.Println(slices.Collect(maps.Keys(nm)))
	// iter.Pull over a host-map seq works through the same source
	next, stop := iter.Pull(maps.Values(map[string]int{"x": 9}))
	v, ok := next()
	fmt.Println(v, ok)
	stop()
}
