package main

import (
	"fmt"
	"sync"
)

type S struct{ N int }

func (s *S) Tick() int { s.N++; return s.N }

type P *S

func main() {
	s := S{}
	var a P = &s
	p := sync.Pool{}
	p.Put(a)
	fmt.Println(s.N)
	q := sync.Pool{New: func() any { return a }}
	_ = q.Get()
	fmt.Println(s.N)
}
