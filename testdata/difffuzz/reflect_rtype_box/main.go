package main

// a boxed facade reflect.Type reflects like gc's *rtype: Type(),
// Kind(), and Interface() on the box of a host-backed or script-typed
// RType must not leak the internal *minireflect.RType spelling.

import (
	"fmt"
	"reflect"
)

type myInt int

func main() {
	t := reflect.TypeOf(0)
	fmt.Println(reflect.ValueOf(t).Type())      // *reflect.rtype
	fmt.Println(reflect.ValueOf(t).Kind())      // ptr
	fmt.Println(reflect.ValueOf(t).Interface()) // int (the type itself)
	// script-declared type → facade RType without a host descriptor
	st := reflect.TypeOf(myInt(0))
	fmt.Println(reflect.ValueOf(st).Type())
	fmt.Println(reflect.ValueOf(st).Interface())
	// nested: reflect.TypeOf on a reflect.Type value
	fmt.Println(reflect.TypeOf(t))
}
