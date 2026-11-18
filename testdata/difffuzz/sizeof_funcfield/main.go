package main

import (
	"fmt"
	"unsafe"
)

func boom() int { panic("must not evaluate") }

func main() {
	p := struct{ F func() int }{F: boom}
	fmt.Println(unsafe.Sizeof(p.F()))
}
