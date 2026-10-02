package main

import (
	"fmt"
	"unsafe"
)

func try(f func()) (s string) {
	defer func() {
		if r := recover(); r != nil {
			s = r.(error).Error()
		}
	}()
	f()
	return "no panic"
}

func main() {
	var p *byte
	fmt.Println(unsafe.Sizeof(p))
	fmt.Println(unsafe.Sizeof(int8(0)), unsafe.Sizeof(rune(0)))
	fmt.Println(try(func() { _ = make([]int, 1<<60) }))
	fmt.Println(try(func() { _ = make([]int, 0, 1<<60) }))
	n := -1
	fmt.Println(try(func() { _ = make([]int, n) }))
	fmt.Println(try(func() { _ = make([]byte, 4) }))
}
