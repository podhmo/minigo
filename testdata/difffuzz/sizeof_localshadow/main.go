package main

import (
	"fmt"
	"unsafe"
)

var _ = unsafe.Sizeof(0)

var n int

func flag() bool { n++; return true }

func main() {
	unsafe := struct{ Sizeof func(bool) bool }{
		Sizeof: func(b bool) bool {
			fmt.Println("local called", b)
			return b
		},
	}
	fns := map[string]func() bool{"flag": flag}
	fmt.Println(unsafe.Sizeof(!fns["flag"]()))
	fmt.Println(n)
}
