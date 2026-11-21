package main

import (
	"fmt"
	"sync/atomic"
)

type C int32

func main() {
	var cv C = 1
	atomic.AddInt32((*int32)(&cv), 2)
	fmt.Println(cv)
	fmt.Printf("%T\n", cv)
	atomic.StoreInt32((*int32)(&cv), 7)
	fmt.Println(cv)
	fmt.Printf("%T\n", cv)
	var x int32 = 1
	atomic.AddInt32(&x, 2)
	fmt.Printf("%T %v\n", x, x)
	unary()
}

func unary() {
	var x int32 = 1
	atomic.AddInt32(&x, 2)
	fmt.Println(-x)
	var cv C = 1
	atomic.AddInt32((*int32)(&cv), 2)
	fmt.Println(-cv)
	fmt.Println(cv + 1)
}
