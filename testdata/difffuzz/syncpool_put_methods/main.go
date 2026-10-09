package main

import (
	"fmt"
	"sync"
)

// Reader has niladic methods with side effects, like a json Decoder's
// ReadToken: putting it into a pool must not call them.
type Reader struct {
	pos  int
	data []string
}

func (r *Reader) Next() string {
	if r.pos >= len(r.data) {
		return ""
	}
	r.pos++
	return r.data[r.pos-1]
}

func (r *Reader) Pos() int { return r.pos }

var pool = sync.Pool{New: func() any { return &Reader{} }}

func main() {
	r := pool.Get().(*Reader)
	r.data = []string{"a", "b", "c"}
	fmt.Println(r.Next(), r.Pos())
	pool.Put(r)
	fmt.Println(r.Pos(), r.Next())

}
