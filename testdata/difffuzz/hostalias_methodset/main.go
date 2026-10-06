package main

import (
	"fmt"
	"sync"
)

type A = sync.Mutex

func main() {
	var p *A
	var l sync.Locker = p
	fmt.Println(l != nil)
	var m A
	l = &m
	l.Lock()
	l.Unlock()
	fmt.Println("unlocked")
}
