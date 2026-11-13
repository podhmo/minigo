package main

import (
	"fmt"
	"slices"
	"strings"
)

type Celsius float64
type IDs []int
type Rec struct {
	Name string
	Age  int
}

func try(tag string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v (%T)\n", tag, r, r)
		}
	}()
	f()
}

func main() {
	var nilInts []int
	var trace []string
	// slices.CompactFunc
	s := []string{"a", "A", "b", "B", "b", "c"}
	c := slices.CompactFunc(s, strings.EqualFold)
	fmt.Println(c, len(c), cap(c), s)
	nums := []int{1, 2, 4, 5, 7, 8, 9}
	fmt.Println(slices.CompactFunc(nums, func(a, b int) bool { return a-b == 1 }), nums)
	same := []int{1, 2, 3}
	sc := slices.CompactFunc(same, func(a, b int) bool { return false })
	fmt.Println(sc, len(sc), cap(sc))
	fmt.Println(slices.CompactFunc([]int{7}, func(a, b int) bool { return true }), slices.CompactFunc([]int(nil), func(a, b int) bool { return true }) == nil)
	fmt.Println(slices.CompactFunc(IDs{1, 1, 2, 2}, func(a, b int) bool { return a == b }))
	trace = nil
	slices.CompactFunc([]int{1, 1, 2, 2, 3}, func(a, b int) bool {
		trace = append(trace, fmt.Sprint(a, b))
		return a == b
	})
	fmt.Println(trace)
	rs := []Rec{{"x", 1}, {"y", 1}, {"z", 2}}
	rc := slices.CompactFunc(rs, func(a, b Rec) bool { return a.Age == b.Age })
	fmt.Println(rc, rs, rs[:3])
	full := rs[:cap(rs)]
	fmt.Println(full[2])

	// slices.ContainsFunc
	fmt.Println(slices.ContainsFunc([]int{1, 2, 3}, func(x int) bool { return x == 2 }), slices.ContainsFunc([]int{1, 2, 3}, func(x int) bool { return x > 5 }))
	fmt.Println(slices.ContainsFunc(nilInts, func(x int) bool { return true }), slices.ContainsFunc(IDs{4}, func(x int) bool { return x == 4 }))
	trace = nil
	slices.ContainsFunc([]string{"a", "b", "c"}, func(x string) bool {
		trace = append(trace, x)
		return x == "b"
	})
	fmt.Println(trace)

	// slices.Replace
	r1 := []int{1, 2, 3, 4, 5}
	fmt.Println(slices.Replace(r1, 1, 3, 9), r1)
	r2 := []int{1, 2, 3, 4, 5}
	fmt.Println(slices.Replace(r2, 1, 3, 7, 8, 9), r2)
	r3 := []int{1, 2, 3, 4, 5}
	fmt.Println(slices.Replace(r3, 3, 5, 6, 7), r3, slices.Replace(r3, 0, 0, 0), slices.Replace(r3, 2, 2))
	r4 := []int{1, 2, 3}
	r4b := slices.Replace(r4, 1, 2, 10, 11, 12)
	fmt.Println(r4b, r4, len(r4b))
	r5 := make([]int, 3, 10)
	copy(r5, []int{1, 2, 3})
	r5b := slices.Replace(r5, 0, 3, 4, 5, 6, 7)
	fmt.Println(r5b, cap(r5b) == 10, r5[:3])
	r6 := []int{1, 2, 3, 4, 5}
	r6b := slices.Replace(r6, 1, 4, 0)
	fmt.Println(r6b, r6, r6[:5], len(r6b), cap(r6b))
	r7 := []int{1, 2, 3, 4, 5}
	fmt.Println(slices.Replace(r7, 2, 5), r7[:5])
	fmt.Println(slices.Replace(r7[:0], 0, 0, 1), slices.Replace([]int(nil), 0, 0, 1, 2), slices.Replace([]int(nil), 0, 0))
	fmt.Println(slices.Replace(IDs{1, 2, 3}, 0, 1, 9, 9), slices.Replace([]Celsius{1, 2}, 0, 1, 3, 4.5), slices.Replace([]float64{1, 2}, 1, 2, 3))
	rs2 := []Rec{{"a", 1}, {"b", 2}, {"c", 3}}
	rr := slices.Replace(rs2, 0, 2, Rec{"z", 0})
	fmt.Println(rr, rs2, rs2[:3])
	words := []string{"a", "b", "c"}
	fmt.Println(slices.Replace(words, 1, 2, words...), words)
	fmt.Println(slices.Replace(words, 0, 1, []string{"p", "q"}...))
	anys := []any{1, "two", 3.0}
	fmt.Println(slices.Replace(anys, 0, 2, 7, nil, "x"))
	try("replace-j-cap", func() { slices.Replace([]int{1, 2, 3}, 0, 5, 1) })
	try("replace-i-gt-j", func() { slices.Replace([]int{1, 2, 3}, 2, 1, 1) })
	try("replace-neg-i", func() { slices.Replace([]int{1, 2, 3}, -1, 1, 1) })
	try("replace-neg-j", func() { slices.Replace([]int{1, 2, 3}, 0, -1, 1) })
	try("replace-j-gt-len", func() { fmt.Println(slices.Replace(make([]int, 2, 5), 0, 4, 1)) })
	try("replace-nil-oob", func() { slices.Replace(nilInts, 0, 1, 1) })
}
