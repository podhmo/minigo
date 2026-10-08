package main

import (
	"fmt"
	"sync"
)

// Counter has niladic methods with side effects: a pool miss that
// builds one through New must not call them.
type Counter struct {
	calls *int
}

func (c *Counter) Bump() int {
	*c.calls++
	return *c.calls
}

func (c Counter) Calls() int { return *c.calls }

var made int

var pool = sync.Pool{New: func() any {
	made++
	n := 0
	return &Counter{calls: &n}
}}

func main() {
	c := pool.Get().(*Counter)
	fmt.Println(made, c.Calls())
	fmt.Println(c.Bump(), c.Calls())

	var p sync.Pool
	p.New = func() any {
		n := 10
		return Counter{calls: &n}
	}
	v := p.Get().(Counter)
	fmt.Println(v.Calls())
}
