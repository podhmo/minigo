package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
)

var (
	v_ss_2 []string = []string{"b", "a", "c"}
	v_m_0  map[string]int = nil
	v_m_2  map[string]int = map[string]int{"a": 1, "b": 2, "c": 3}
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func main() {
	try(0, func() any { return maps.Clone(v_m_0) == nil })
	try(1, func() any { c := maps.Clone(v_m_2); if c == nil { c = map[string]int{} }; c["x"]++; return c })
	try(2, func() any { c := slices.Clone(v_ss_2); sort.Strings(c); return c })
}
