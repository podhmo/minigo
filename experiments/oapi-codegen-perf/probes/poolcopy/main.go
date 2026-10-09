package main

import (
	"fmt"
	"sync"
)

type S struct{ N int }

func main() { p := sync.Pool{}; s := S{N: 1}; p.Put(s); s.N = 2; fmt.Println(p.Get().(S).N) }
