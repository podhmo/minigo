package main

import (
	"fmt"
	"sync/atomic"
)

func main() {
	var n atomic.Uint32
	n.Store(2)
	s := []int{10, 11, 12, 13, 14}
	fmt.Println(s[n.Load():])
	fmt.Println(s[n.Load()])
	str := "hello"
	fmt.Println(str[n.Load():])

	var p atomic.Uintptr
	p.Store(1)
	fmt.Println(s[:p.Load()])
}
