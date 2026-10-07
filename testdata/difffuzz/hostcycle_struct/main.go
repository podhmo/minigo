package main

import (
	"fmt"
	"sync"
)

// a self-referencing struct marshals for a host `any` parameter by
// walking fields — the visited set folds the cycle back to the
// projection already built, where unbounded recursion used to
// overflow the host stack.
type Node struct{ Next *Node }

func main() {
	// sync.Map keeps the round-trip deterministic — sync.Pool could
	// drop the entry between Put and Get and flake the assertion.
	var m sync.Map
	x := &Node{}
	x.Next = x
	m.Store("x", x)
	got, _ := m.Load("x")
	fmt.Println(got.(*Node) == x)

	// a two-node cycle folds the same way.
	a, b := &Node{}, &Node{}
	a.Next, b.Next = b, a
	m.Store("a", a)
	got, _ = m.Load("a")
	fmt.Println(got.(*Node) == a)
}
