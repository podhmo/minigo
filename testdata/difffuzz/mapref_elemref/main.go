package main

import "fmt"

type s struct{ x int }
type sl []int

func main() {
	// reference-shaped map elements support interior writes.

	mp := map[string]*s{"a": {x: 1}}
	mp["a"].x += 9 // pointer deref
	fmt.Println(mp["a"].x)

	ms := map[string][]int{"a": {1, 2}}
	ms["a"][0] = 9
	fmt.Println(ms["a"])

	mss := map[string][]s{"a": {{x: 1}}}
	mss["a"][0].x = 9
	fmt.Println(mss["a"][0].x)

	mn := map[string]sl{"a": {1, 2}}
	mn["a"][1] = 9
	fmt.Println(mn["a"])

	mm := map[string]map[string]int{"a": {"x": 1}}
	mm["a"]["x"] += 4
	fmt.Println(mm["a"]["x"])

	mi := map[string]int{"a": 1}
	mi["a"] += 4
	mi["a"]++
	fmt.Println(mi["a"])
}
