package main

import (
	"fmt"
	"unsafe"
)

func main() {
	type T int8 // local shadowing the package type
	fmt.Println(int(unsafe.Sizeof(V)))
}
