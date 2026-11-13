package main

import (
	"fmt"
	"io/fs"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
)

type Celsius float64
type Scores map[string]int
type Mode int
type Rec struct {
	Name string
	Age  int
}
type IDs []int

func try(tag string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v (%T)\n", tag, r, r)
		}
	}()
	f()
}

func sortedKeys[M ~map[K]V, K ~int | ~string, V any](m M) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

func main() {
	// maps.EqualFunc
	eqLen := func(a string, b int) bool { return len(a) == b }
	fmt.Println(maps.EqualFunc(map[int]string{1: "a", 2: "bb"}, map[int]int{1: 1, 2: 2}, eqLen))
	fmt.Println(maps.EqualFunc(map[int]string{1: "a", 2: "bb"}, map[int]int{1: 1, 2: 3}, eqLen))
	fmt.Println(maps.EqualFunc(map[int]string{1: "a"}, map[int]int{1: 1, 2: 2}, eqLen), maps.EqualFunc(map[int]string{1: "a"}, map[int]int{3: 1}, eqLen))
	var nilm map[int]string
	fmt.Println(maps.EqualFunc(nilm, map[int]int{}, eqLen), maps.EqualFunc(map[int]string{}, map[int]int(nil), eqLen), maps.EqualFunc(nilm, map[int]int{1: 1}, eqLen))
	fmt.Println(maps.EqualFunc(Scores{"a": 1}, map[string]float64{"a": 1.0}, func(a int, b float64) bool { return float64(a) == b }))
	fmt.Println(maps.EqualFunc(map[Mode]string{1: "x"}, map[Mode]string{1: "X"}, strings.EqualFold))
	fmt.Println(maps.EqualFunc(map[fs.FileMode]int{fs.ModeDir: 1}, map[fs.FileMode]int{fs.ModeDir: 1}, func(a, b int) bool { return a == b }))
	nan := math.NaN()
	nm := map[float64]int{nan: 1}
	fmt.Println(maps.EqualFunc(nm, nm, func(a, b int) bool { return true }))
	fmt.Println(maps.EqualFunc(map[string]Rec{"a": {"x", 1}}, map[string]Rec{"a": {"y", 1}}, func(a, b Rec) bool { return a.Age == b.Age }))
	calls := 0
	fmt.Println(maps.EqualFunc(map[int]int{1: 1, 2: 2, 3: 3}, map[int]int{1: 1, 2: 2, 3: 3}, func(a, b int) bool { calls++; return false }), calls)
	calls = 0
	fmt.Println(maps.EqualFunc(map[int]int{1: 1}, map[int]int{1: 1, 2: 2}, func(a, b int) bool { calls++; return true }), calls)
	try("equalfunc-panic", func() {
		maps.EqualFunc(map[int]int{1: 1}, map[int]int{1: 1}, func(a, b int) bool { panic("boom") })
	})
	ia := map[string]any{"a": 1, "b": "x"}
	ib := map[string]any{"a": 1, "b": "x"}
	fmt.Println(maps.EqualFunc(ia, ib, func(a, b any) bool { return a == b }))

	// maps.DeleteFunc
	m := map[int]string{1: "a", 2: "bb", 3: "cc", 4: "d"}
	maps.DeleteFunc(m, func(k int, v string) bool { return len(v) == 2 })
	fmt.Println(m, len(m))
	maps.DeleteFunc(m, func(k int, v string) bool { return k > 100 })
	fmt.Println(m)
	maps.DeleteFunc(m, func(k int, v string) bool { return true })
	fmt.Println(m, len(m), m == nil)
	var nm2 map[string]int
	maps.DeleteFunc(nm2, func(k string, v int) bool { return true })
	fmt.Println(nm2 == nil, len(nm2))
	sc := Scores{"a": 1, "b": 2, "c": 3}
	maps.DeleteFunc(sc, func(k string, v int) bool { return v%2 == 1 })
	fmt.Println(sc, sortedKeys(sc))
	modes := map[Mode]string{1: "x", 2: "y", 3: "z"}
	maps.DeleteFunc(modes, func(k Mode, v string) bool { return k == 2 })
	fmt.Println(modes, sortedKeys(modes))
	fm := map[fs.FileMode]int{fs.ModeDir: 1, fs.ModeSymlink: 2}
	maps.DeleteFunc(fm, func(k fs.FileMode, v int) bool { return k == fs.ModeDir })
	fmt.Println(len(fm), fm[fs.ModeSymlink])
	cm := map[Celsius]bool{1.5: true, 2.5: false}
	maps.DeleteFunc(cm, func(k Celsius, v bool) bool { return !v })
	fmt.Println(cm)
	nanm := map[float64]int{nan: 1, 1: 2}
	maps.DeleteFunc(nanm, func(k float64, v int) bool { return true })
	fmt.Println(len(nanm))
	recs := map[string]Rec{"a": {"x", 1}, "b": {"y", 2}}
	maps.DeleteFunc(recs, func(k string, v Rec) bool { return v.Age > 1 })
	fmt.Println(recs)
	an := map[any]int{1: 1, "s": 2, 2.5: 3}
	maps.DeleteFunc(an, func(k any, v int) bool { _, isStr := k.(string); return isStr })
	fmt.Println(len(an), an[1], an[2.5])
	var seenKeys []string
	km := map[string]int{"a": 1}
	maps.DeleteFunc(km, func(k string, v int) bool { seenKeys = append(seenKeys, fmt.Sprintf("%s=%d", k, v)); return false })
	fmt.Println(seenKeys, km)
	// the callback may delete other entries before their turn
	only := map[int]int{1: 1, 2: 2, 3: 3}
	visited := 0
	maps.DeleteFunc(only, func(k, v int) bool {
		visited++
		for other := range only {
			if other != k {
				delete(only, other)
			}
		}
		return false
	})
	fmt.Println(visited, len(only))
	try("deletefunc-panic", func() {
		maps.DeleteFunc(map[int]int{1: 1}, func(k, v int) bool { panic("boom") })
	})
	// shared contents: a clone is independent, an alias is not
	orig := map[int]int{1: 1, 2: 2}
	alias := orig
	cl := maps.Clone(orig)
	maps.DeleteFunc(alias, func(k, v int) bool { return k == 1 })
	fmt.Println(orig, alias, cl)

	// the existing DeleteFunc/Compact tails: struct elements are zeroed whole
	rs := []Rec{{"a", 1}, {"b", 2}, {"c", 3}}
	kept := slices.DeleteFunc(rs, func(r Rec) bool { return r.Age == 2 })
	fmt.Println(kept, rs[:3])
	cs := []Rec{{"a", 1}, {"a", 1}, {"b", 2}}
	ck := slices.Compact(cs)
	fmt.Println(ck, cs[:3])
}
