package main

import (
	"fmt"
	"reflect"
)

func main() {
	// gc rejects a reflect.Value `*` operand (BADPREC); the marshalled
	// form loses the reflect.Value box so the host reads its inner int.
	fmt.Println(fmt.Errorf("%*.*s", "x", reflect.ValueOf(2), "y"))
	fmt.Println(fmt.Errorf("%*.*s", 3, reflect.ValueOf(2), "y"))
}
