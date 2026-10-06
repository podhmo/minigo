package main

// reflect.Value.MapRange must produce NaN keys too: Go's iterator walks
// buckets, not lookups, so entries reachable only by iteration (NaN can
// never be Get'd) still appear. Presence during iteration probes the
// stored canonical key, not a fresh re-canonicalization.

import (
	"fmt"
	"math"
	"reflect"
)

func main() {
	m := map[float64]int{}
	for i := 0; i < 10; i++ {
		m[math.NaN()] = i
	}
	v := reflect.ValueOf(m)
	it := v.MapRange()
	n := 0
	for it.Next() {
		n++
		_ = it.Key()
		_ = it.Value()
	}
	fmt.Println(n, len(m)) // 10 10

	// a key deleted mid-iteration is still skipped
	k := map[int]int{0: 0, 1: 1, 2: 2}
	vk := reflect.ValueOf(k)
	it2 := vk.MapRange()
	seen := 0
	for it2.Next() {
		seen++
		if seen == 1 {
			delete(k, it2.Key().Interface().(int))
		}
	}
	fmt.Println(seen, len(k)) // 3 2
}
