package main

import (
	"fmt"
	"unsafe"
)

// no `dep` import here — `dep.T` inside V's initializer belongs to
// the declaring file's scope, not this one.
func main() {
	fmt.Println(int(unsafe.Sizeof(V)))
}
