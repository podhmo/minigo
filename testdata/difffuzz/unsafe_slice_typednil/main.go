package main

import (
	"fmt"
	"unsafe"
)

// A typed nil pointer is still nil: unsafe.Slice(p, 0) and
// unsafe.String(p, 0) are legal and report the nil/empty result — Go
// panics only when the length is not zero.
func main() {
	var p *byte
	fmt.Println(unsafe.Slice(p, 0) == nil, unsafe.String(p, 0) == "")
	var q *int
	fmt.Println(unsafe.Slice(q, 0) == nil)
}
