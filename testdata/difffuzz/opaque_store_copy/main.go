package main

import (
	"fmt"
	"sync"
)

// An `any` storage box snapshots its value like gc's interface boxing:
// a script struct stored into sync.Pool / sync.Map is copied at the
// boundary — mutating the source after the store does not leak, and
// mutating the source key does not move the entry.
type S struct{ N int }
type K struct{ A string }
type U struct{ Xs []int } // unhashable — crosses verbatim, still copied

func main() {
	var p sync.Pool
	s := S{1}
	p.Put(s)
	s.N = 2
	fmt.Println(p.Get().(S).N)

	var m sync.Map
	k := K{"a"}
	m.Store(k, s)
	k.A = "b"
	got, _ := m.Load(K{"a"})
	fmt.Println(got.(S).N)
	s.N = 9
	g2, _ := m.Load(K{"a"})
	fmt.Println(g2.(S).N)

	m.Store("u", U{[]int{1}})
	fmt.Println(m.Load("u"))
}
