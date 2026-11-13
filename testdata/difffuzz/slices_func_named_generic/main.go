package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

type IDs []int
type Rec struct {
	Name string
	Age  int
}

func oldest[S ~[]E, E any](s S, by func(a, b E) int) E {
	return slices.MaxFunc(s, by)
}

func dedupe[S ~[]E, E comparable](s S) S {
	return slices.CompactFunc(s, func(a, b E) bool { return a == b })
}

func main() {
	// result types keep the named slice type
	ids := IDs{1, 1, 2, 2, 3}
	fmt.Printf("%T %v\n", slices.CompactFunc(ids, func(a, b int) bool { return a == b }), ids[:3])
	fmt.Printf("%T %v\n", slices.Replace(IDs{1, 2, 3}, 0, 1, 7, 8), IDs{})
	fmt.Printf("%T %T\n", slices.Replace([]string{"a"}, 0, 1, "b"), slices.CompactFunc([]string{"a"}, func(a, b string) bool { return a == b }))
	fmt.Printf("%T %T %T\n", slices.MaxFunc(IDs{1}, cmp.Compare[int]), slices.Compare(IDs{1}, IDs{2}), slices.IsSortedFunc(IDs{1}, cmp.Compare[int]))
	fmt.Printf("%T %T\n", slices.MaxFunc([]Rec{{"a", 1}}, func(a, b Rec) int { return a.Age - b.Age }), slices.MinFunc([]float64{2, 1}, cmp.Compare[float64]))

	// the result of CompactFunc aliases the argument
	s := []int{1, 1, 2, 3, 3}
	c := slices.CompactFunc(s, func(a, b int) bool { return a == b })
	c[0] = 100
	fmt.Println(s, c)
	// Replace in place shares the backing array
	r := make([]int, 5, 8)
	copy(r, []int{1, 2, 3, 4, 5})
	r2 := slices.Replace(r, 1, 4, 20, 30)
	r2[0] = 99
	fmt.Println(r, r2, len(r2), cap(r2))

	// std callbacks as the comparators
	fmt.Println(slices.MaxFunc([]string{"b", "c", "a"}, strings.Compare), slices.MinFunc([]string{"b", "c", "a"}, strings.Compare))
	fmt.Println(slices.CompareFunc([]string{"a", "b"}, []string{"a", "c"}, strings.Compare), slices.CompareFunc([]int{1, 2}, []int{1, 2}, cmp.Compare[int]))
	fmt.Println(slices.IsSortedFunc([]string{"a", "b"}, strings.Compare), slices.IsSortedFunc([]string{"b", "a"}, strings.Compare))
	fmt.Println(slices.CompactFunc([]string{"a", "A", "b"}, strings.EqualFold))
	fmt.Println(slices.ContainsFunc([]string{"Go", "Rust"}, func(s string) bool { return strings.HasPrefix(s, "R") }))

	// through generic wrappers
	fmt.Println(oldest([]Rec{{"a", 3}, {"b", 9}}, func(a, b Rec) int { return cmp.Compare(a.Age, b.Age) }), oldest(IDs{4, 2}, cmp.Compare[int]))
	fmt.Println(dedupe([]string{"x", "x", "y"}), dedupe(IDs{5, 5, 5}))

	// composing: nested slices
	grid := [][]int{{1, 2}, {1, 2}, {1, 3}}
	fmt.Println(slices.CompactFunc(grid, func(a, b []int) bool { return slices.Equal(a, b) }), len(grid))
	fmt.Println(slices.MaxFunc(grid, func(a, b []int) int { return slices.Compare(a, b) }), slices.MinFunc(grid, func(a, b []int) int { return slices.Compare(a, b) }))
	fmt.Println(slices.IsSortedFunc(grid, func(a, b []int) int { return slices.Compare(a, b) }), slices.CompareFunc(grid, [][]int{{1, 2}, {2}}, func(a, b []int) int { return slices.Compare(a, b) }))
	names := [][]string{{"b", "a"}, {"a", "z"}, {"a"}}
	slices.SortFunc(names, func(a, b []string) int { return slices.Compare(a, b) })
	fmt.Println(names)

	// Compare through bound names
	cmpFn := func(a, b []int) int { return slices.Compare(a, b) }
	fmt.Println(cmpFn([]int{1}, []int{2}), slices.CompareFunc([][]int{{1}}, [][]int{{1}}, cmpFn))

	// byte and rune slices
	fmt.Println(slices.Compare([]byte("go"), []byte("gp")), slices.Replace([]byte("hello"), 0, 1, 'J'), string(slices.Replace([]byte("hello"), 1, 2, []byte("ippy")...)))
	// floats
	fmt.Println(slices.MaxFunc([]float64{1.5, 2.5, 0.5}, cmp.Compare[float64]), slices.Compare([]float32{1.5}, []float32{2.5}))
	// uint8 and int8 elements
	fmt.Println(slices.Compare([]uint8{200}, []uint8{100}), slices.Compare([]int8{-5}, []int8{3}), slices.Compare([]uint32{1 << 31}, []uint32{1}))
}
