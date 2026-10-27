package main

// control flow through an intrinsic seq's loop body: an early return,
// a recovered panic, continue / labeled break, a type assertion to
// iter.Seq[K], and key types that convert back (float64, int8, struct,
// interface keys).

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
)

func find(m map[string]int, want int) string {
	for k := range maps.Keys(m) {
		if m[k] == want {
			return k
		}
	}
	return "none"
}

func safe(m map[string]int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	for range maps.Values(m) {
		panic("boom")
	}
	return nil
}

func main() {
	m := map[string]int{"a": 1, "b": 2, "c": 3}
	fmt.Println(find(m, 2), find(m, 9))
	fmt.Println(safe(m))
	total := 0
outer:
	for i := range 3 {
		for v := range maps.Values(m) {
			if v == 2 {
				continue
			}
			if i == 2 {
				break outer
			}
			total += v
		}
	}
	fmt.Println(total)
	var x any = maps.Keys(m)
	if s, ok := x.(iter.Seq[string]); ok {
		fmt.Println("asserted", len(slices.Collect(s)))
	}
	_, ok := x.(iter.Seq[int])
	fmt.Println(ok)
	var ks iter.Seq[string] = maps.Keys(m)
	fmt.Println(len(slices.Sorted(ks)))
	for p := range strings.SplitSeq("a,b,c", ",") {
		if p == "b" {
			break
		}
		fmt.Println(p)
	}
	fmt.Println(slices.Collect(strings.SplitSeq("a,b", ",")))
	fmt.Println(slices.Sorted(maps.Keys(map[float64]bool{2.5: true, 1: true})))
	fmt.Println(slices.Sorted(maps.Keys(map[int8]bool{2: true, -1: true})))
	type P struct{ X, Y int }
	pm := map[P]string{{1, 2}: "a"}
	fmt.Printf("%v %T\n", slices.Collect(maps.Keys(pm)), slices.Collect(maps.Keys(pm)))
	im := map[any]int{1: 1, "s": 2}
	n := 0
	for k := range maps.Keys(im) {
		if _, ok := k.(int); ok {
			n++
		}
	}
	fmt.Println(n)
	fmt.Printf("%T\n", slices.Collect(maps.Values(map[string][]int{"a": {1}})))
}
