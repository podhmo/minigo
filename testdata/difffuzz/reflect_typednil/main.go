package main

import (
	"fmt"
	"reflect"
)

// reflect.ValueOf((*byte)(nil)) inside a function called during package
// init must be a typed-nil pointer Value, not a zero Value.
// $GOROOT/test/fixedbugs/issue30606b.go.

func typ(x interface{}) reflect.Type { return reflect.ValueOf(x).Type() }

var ptrType = typ((*byte)(nil))

func main() {
	fmt.Println(ptrType)
}
