package main

import (
	"fmt"
	"reflect"
)

var bx = [16]byte{1, 0, 0, 0, 1, 1, 1, 1, 0, 0, 0, 0, 1, 1, 0, 0}
var b6 = [...]byte{1, 4: 1, 1, 1, 1, 12: 1, 1, 0, 0}
var e8 = [...]byte{1, 4: 1, 1, 1, 1}
var s8 = [8]byte{1, 4: 1, 1, 1, 1}

func main() {
	fmt.Println(reflect.DeepEqual(bx, b6))
	fmt.Println(reflect.DeepEqual(e8, s8))
	// keyed byte elems carry the declared tag in %T-like contexts
	f := [4]byte{'a', 2: 'c'}
	g := [4]byte{'a', 0, 'c', 0}
	fmt.Println(reflect.DeepEqual(f, g))
	fmt.Println(bx)
}
