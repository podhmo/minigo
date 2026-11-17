package main

import (
	"fmt"
	"unsafe"
)

type Pair struct {
	A int64
	B int32
}

func f() int { fmt.Println("f ran"); return 1 }

func main() {
	var p *Pair
	s := unsafe.Sizeof(f())
	fmt.Println("after Sizeof")
	fmt.Printf("%T %v\n", s, s)
	fmt.Printf("%T %v\n", unsafe.Sizeof([3]int64{}), unsafe.Sizeof([3]int64{}))
	fmt.Printf("%v\n", unsafe.Sizeof(p.A)) // field of nil ptr: must not eval
	fmt.Printf("%T %v\n", unsafe.Alignof(p.B), unsafe.Alignof(p.B))
	fmt.Printf("%T %v\n", unsafe.Offsetof(p.B), unsafe.Offsetof(p.B))
	var err error = fmt.Errorf("x")
	fmt.Printf("%v\n", unsafe.Sizeof(err))
	fmt.Printf("%v\n", unsafe.Sizeof(1))
	fmt.Printf("%v\n", unsafe.Sizeof("abc"))
	var arr [4]int32
	fmt.Printf("%v %v\n", unsafe.Sizeof(arr), unsafe.Sizeof(arr[2]))
}
