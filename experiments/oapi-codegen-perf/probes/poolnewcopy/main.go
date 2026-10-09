package main

import (
	"fmt"
	"sync"
)

type S struct{ N int }

func main() {
	s := S{N: 1}
	p := sync.Pool{New: func() any { return s }}
	a := p.Get().(S)
	s.N = 2
	fmt.Println(a.N)
}
