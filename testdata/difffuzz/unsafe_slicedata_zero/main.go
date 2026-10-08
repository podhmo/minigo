package main

import (
	"fmt"
	"unsafe"
)

// unsafe.SliceData on a length-0 slice with spare capacity must yield a
// pointer to the backing array, not nil — unsafe.Slice then reads and
// writes through it.
func main() {
	b := make([]byte, 0, 4)
	p := unsafe.SliceData(b)
	fmt.Println(p == nil)

	s := unsafe.Slice(p, 2)
	s[0] = 'X'
	s[1] = 'Y'
	fmt.Println(string(b[:2]), len(s))

	var nilb []byte
	fmt.Println(unsafe.SliceData(nilb) == nil)
}
