package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// An `any` storage box snapshots its value like gc's interface boxing:
// a script struct stored into atomic.Value / sync.Pool / sync.Map is
// copied at the boundary — mutating the source after the store does not
// leak, and mutating the source key does not move the entry.
//
// Every line must be deterministic under gc: sync.Pool is allowed to
// drop what it holds (GC, a goroutine migrating to another P, the race
// detector), so a Get right after a Put is never guaranteed to return
// the stored item. atomic.Value is the strict pin; the Pool line only
// accepts "snapshot or fresh", never an alias.
type S struct{ N int }
type K struct{ A string }
type U struct{ Xs []int } // unhashable — crosses verbatim, still copied

func main() {
	s := S{1}

	var av atomic.Value
	av.Store(s)

	p := sync.Pool{New: func() any { return S{-1} }}
	p.Put(s)

	s.N = 2
	fmt.Println(av.Load().(S).N)
	// 1 when the pool kept the snapshot, -1 when it dropped it and New
	// ran; 2 would be an alias of the live source.
	fmt.Println(p.Get().(S).N != 2)

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
