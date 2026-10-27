package main

// maps.Keys / maps.Values hand back an iter.Seq, not a slice: ranging
// with one var yields the keys (not indices), %T spells iter.Seq[K],
// the seq passes to a script func taking iter.Seq[K] (T inferred), a
// direct call stops when yield reports false, and slices.Sorted /
// SortedFunc / Collect type their result []K after the seq. Also the
// rest of the slices/maps iterator surface: Values, All, Backward,
// Collect, AppendSeq, Chunk, SortedStableFunc, maps.All/Insert/Collect.

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"time"
)

type Color string

type Pair struct {
	K string
	V int
}

func collect[T any](seq iter.Seq[T]) []T {
	var out []T
	for v := range seq {
		out = append(out, v)
	}
	return out
}

func count[K comparable](seq iter.Seq[K]) int {
	n := 0
	for range seq {
		n++
	}
	return n
}

func mapSeq[T, U any](seq iter.Seq[T], f func(T) U) iter.Seq[U] {
	return func(yield func(U) bool) {
		for v := range seq {
			if !yield(f(v)) {
				return
			}
		}
	}
}

func main() {
	one := map[string]bool{"a": true}
	for k := range maps.Keys(one) {
		fmt.Println("key", k)
	}
	for v := range maps.Values(one) {
		fmt.Println("value", v)
	}
	fmt.Printf("%T %T\n", maps.Keys(one), maps.Values(one))
	fmt.Printf("%T\n", maps.All(map[Color]time.Month{}))

	m := map[string]int{"b": 2, "a": 1, "c": 3}
	ks := slices.Sorted(maps.Keys(m))
	fmt.Printf("%T %v\n", ks, ks)
	vs := slices.Sorted(maps.Values(m))
	fmt.Printf("%T %v\n", vs, vs)
	fmt.Println(slices.Sorted(slices.Values(collect(maps.Keys(m)))))
	fmt.Println(count(maps.Keys(m)), count(maps.Values(m)))

	months := map[time.Month]int{time.March: 1, time.January: 2}
	mk := slices.Sorted(maps.Keys(months))
	fmt.Printf("%T %v\n", mk, mk)
	colors := map[Color]bool{"red": true, "blue": false}
	ck := slices.Sorted(maps.Keys(colors))
	fmt.Printf("%T %v\n", ck, ck)
	cs := slices.SortedFunc(maps.Keys(colors), func(a, b Color) int { return -strings.Compare(string(a), string(b)) })
	fmt.Printf("%T %v\n", cs, cs)

	// a direct call: returning false from yield stops the producer
	calls := 0
	maps.Keys(m)(func(k string) bool {
		calls++
		return false
	})
	fmt.Println("calls", calls)
	n := 0
	for range maps.Keys(m) {
		n++
		if n == 2 {
			break
		}
	}
	fmt.Println("broke at", n)

	// nested / generic composition
	upper := mapSeq(maps.Keys(m), strings.ToUpper)
	fmt.Printf("%T %v\n", upper, slices.Sorted(upper))
	lens := slices.Collect(mapSeq(slices.Values([]string{"x", "yy"}), func(s string) int { return len(s) }))
	fmt.Printf("%T %v\n", lens, lens)

	// empty and nil maps
	var nilMap map[string]int
	fmt.Println(count(maps.Keys(nilMap)), count(maps.Values(map[int]string{})))
	es := slices.Sorted(maps.Keys(nilMap))
	fmt.Printf("%T %v %v\n", es, es, es == nil)
	ec := slices.Collect(maps.Values(map[int]string{}))
	fmt.Printf("%T %v %v\n", ec, ec, ec == nil)

	// deleting entries not yet reached: they are not produced
	mm := map[int]bool{1: true, 2: true, 3: true, 4: true}
	seen := 0
	for k := range maps.Keys(mm) {
		seen++
		for j := range 5 {
			if j != k {
				delete(mm, j)
			}
		}
	}
	fmt.Println("seen", seen, len(mm))
	// deleting the current key each turn still visits every entry once
	dm := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}
	visited := 0
	for k := range maps.Keys(dm) {
		visited++
		delete(dm, k)
	}
	fmt.Println("visited", visited, len(dm))
	// values read while other keys are deleted and re-added
	vm := map[int]int{1: 10, 2: 20, 3: 30}
	vsum := 0
	for v := range maps.Values(vm) {
		vsum += v
		clear(vm)
	}
	fmt.Println("vsum one", vsum == 10 || vsum == 20 || vsum == 30, len(vm))
	// a value written before its turn reads the new content
	wm := map[string]int{"only": 1}
	for k := range maps.Keys(wm) {
		wm[k] = 42
	}
	for v := range maps.Values(wm) {
		fmt.Println("updated", v)
	}

	// slices iterators
	s := []string{"p", "q", "r"}
	for i, v := range slices.All(s) {
		fmt.Println("all", i, v)
	}
	for i, v := range slices.Backward(s) {
		fmt.Println("back", i, v)
		if i == 1 {
			break
		}
	}
	fmt.Printf("%T %T %T\n", slices.Values(s), slices.All(s), slices.Backward(s))
	fmt.Println(slices.AppendSeq([]string{"o"}, slices.Values(s)))
	for c := range slices.Chunk([]int{1, 2, 3, 4, 5}, 2) {
		fmt.Printf("chunk %T %v\n", c, c)
	}
	ps := []Pair{{"a", 2}, {"b", 1}, {"c", 2}}
	st := slices.SortedStableFunc(slices.Values(ps), func(x, y Pair) int { return x.V - y.V })
	fmt.Printf("%T %v\n", st, st)

	// maps.All / Insert / Collect
	keys := 0
	for k, v := range maps.All(m) {
		keys += v
		_ = k
	}
	fmt.Println("sum", keys)
	cp := maps.Collect(maps.All(m))
	fmt.Printf("%T %v\n", cp, cp)
	cp["z"]++
	fmt.Println(cp["z"], cp["missing"])
	dst := map[string]int{"keep": 0}
	maps.Insert(dst, maps.All(map[string]int{"x": 9}))
	fmt.Println(dst)
	idx := maps.Collect(slices.All([]string{"zero", "one"}))
	fmt.Printf("%T %v\n", idx, idx)
	fmt.Printf("%T\n", maps.Collect(maps.All(colors)))
	fmt.Println(slices.Collect(maps.Keys(map[Color]int{"only": 1})))
}
