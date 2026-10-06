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
	p := sync.Pool{}
	x := &Node{}
	x.Next = x
	p.Put(x)
	fmt.Println(p.Get().(*Node) == x)

	// a two-node cycle folds the same way.
	a, b := &Node{}, &Node{}
	a.Next, b.Next = b, a
	p.Put(a)
	fmt.Println(p.Get().(*Node) == a)
}
