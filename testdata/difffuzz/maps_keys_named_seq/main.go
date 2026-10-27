package main

// maps.Keys/All over maps keyed by named basic types (stored tagged
// since #1021) yield the declared key type: %T of the key, of
// slices.Sorted/Collect results, and maps.Collect/Insert round trips.

import (
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"time"
)

type M int8
type C string

func main() {
	a := map[time.Month]int{time.March: 1, time.January: 2}
	ks := slices.Sorted(maps.Keys(a))
	fmt.Printf("%T %v\n", ks, ks)
	for k := range maps.Keys(a) {
		if k == time.March {
			fmt.Printf("%T %v\n", k, k)
		}
	}
	b := map[M]bool{4: true, -2: true}
	bk := slices.Sorted(maps.Keys(b))
	fmt.Printf("%T %v\n", bk, bk)
	c := map[C]int{"z": 1, "y": 2}
	for k, v := range maps.All(c) {
		if v == 1 {
			fmt.Printf("%T %v\n", k, k)
		}
	}
	fmt.Printf("%v\n", slices.Sorted(maps.Keys(c)))
	d := map[fs.FileMode]bool{fs.ModeDir: true, 0: true}
	dk := slices.Sorted(maps.Keys(d))
	fmt.Printf("%T %v\n", dk, dk)
	cp := maps.Collect(maps.All(c))
	fmt.Printf("%T %v\n", cp, cp)
	dst := map[M]string{}
	maps.Insert(dst, maps.All(map[M]string{3: "x"}))
	fmt.Println(dst[3], len(dst))
	var any1 any = slices.Collect(maps.Keys(b))
	fmt.Printf("%T\n", any1)
}
