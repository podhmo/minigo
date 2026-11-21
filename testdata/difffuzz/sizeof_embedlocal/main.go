package main

import (
	"fmt"
	"unsafe"
)

func main() {
	// a local `Inner` must not steal the promoted lookup — o.Arr's
	// declared field is the package Inner's [4]int64.
	type Inner struct{ Arr [4]int8 }
	fmt.Println(int(unsafe.Sizeof(o.Arr)))
}
