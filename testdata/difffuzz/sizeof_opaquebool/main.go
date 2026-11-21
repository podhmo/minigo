package main

import (
	"fmt"
	"unsafe"
)

func boom() bool  { panic("must not evaluate") }
func boom2() bool { panic("must not evaluate") }

func main() {
	fns := map[string]func() bool{"boom": boom, "boom2": boom2}
	fmt.Println(int(unsafe.Sizeof(!fns["boom"]())))
	fmt.Println(int(unsafe.Alignof(!fns["boom"]() && fns["boom2"]())))
	fmt.Println("ok")
}
