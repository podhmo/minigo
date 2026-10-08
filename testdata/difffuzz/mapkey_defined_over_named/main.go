package main

import "fmt"

type inner struct{ ID int }

// Tag is a defined type over another named struct, like x/text's
// `type Tag compact.Tag`.
type Tag inner

type Box struct{ N int }

func mk(i int) Tag { return Tag(inner{ID: i}) }

func main() {
	m := make(map[Tag]int)
	for i, t := range []Tag{mk(1), mk(2)} {
		m[t] = i
	}
	m[mk(1)] += 10
	mm := map[Tag]map[Tag]int{mk(1): {}}
	mm[mk(1)][mk(2)] = 7
	ms := map[Tag][]int{mk(1): {0, 0}}
	ms[mk(1)][1] = 5
	mp := map[Tag]*Box{mk(1): {}}
	mp[mk(1)].N = 3
	a, b := m[mk(3)], 0
	a, b = 4, 5
	fmt.Println(len(m), m[mk(1)], m[mk(2)], mm[mk(1)][mk(2)], ms[mk(1)], mp[mk(1)].N, a, b)

	type Idx int
	s := []int{1, 2, 3}
	s[Idx(1)] = 9
	p, q := &s[Idx(2)], &s[2]
	fmt.Println(s, p == q)
}
