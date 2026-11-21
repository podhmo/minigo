package main

import (
	"fmt"
	"unsafe"
)

func boom() int { panic("evaluated") }

func main() {
	fmt.Println(unsafe.Sizeof(boom()))
}
