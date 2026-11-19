package main

import (
	"fmt"
	"sync"
)

// one OpSelect site sees different receiver typedefs across iterations:
// the per-site slot must key on TypeID, never on position.
type A struct {
	Tag string
	N   int
}
type B struct {
	N   int
	Tag string
}

func (a A) Name() string { return "A:" + a.Tag }
func (b B) Name() string { return "B:" + b.Tag }

type Namer interface{ Name() string }

func main() {
	// polymorphic selects: the same instructions resolve A.N/B.N at
	// different field indices and A.Name/B.Name in the method set
	names := []Namer{A{Tag: "x", N: 1}, B{Tag: "y", N: 2}, A{Tag: "z", N: 3}}
	for _, n := range names {
		fmt.Println(n.Name())
	}

	// direct field reads through refs — the ref arm forwards the site
	as := []A{{Tag: "p", N: 4}, {Tag: "q", N: 5}}
	for i := range as {
		fmt.Println(as[i].Tag, as[i].N)
	}

	// concurrent selects on one site: goroutines share the chunk and
	// its slot under -race; the total is deterministic under the mutex
	c := &counter{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				c.Bump()
			}
		}()
	}
	wg.Wait()
	fmt.Println("count:", c.Total())
}

type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) Bump() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *counter) Total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
