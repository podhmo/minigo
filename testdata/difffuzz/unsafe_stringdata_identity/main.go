package main

import (
	"fmt"
	"unsafe"
)

// The same string value resolves to the same data pointer across
// unsafe.StringData calls — the backing is interned, not reallocated.
func main() {
	s := "abc"
	fmt.Println(unsafe.StringData(s) == unsafe.StringData(s))
	fmt.Println(unsafe.String(unsafe.StringData(s), 3))

	t := "ab" + "c"
	fmt.Println(unsafe.String(unsafe.StringData(t), 3))
}
