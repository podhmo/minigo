package main

import (
	"bytes"
	"fmt"
	"reflect"
)

// A boxed host value's Type() must report the real reflect.Type — the
// name-only typedef the marshal side stamps has no method set, so
// AssignableTo/Implements against an interface type used to answer
// false for concrete implementations like *bytes.Buffer.
func main() {
	st := reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	bufT := reflect.ValueOf(bytes.NewBufferString("x")).Type()
	fmt.Println("type:", bufT)
	fmt.Println("assignable:", bufT.AssignableTo(st))
	fmt.Println("implements:", bufT.Implements(st))
	fmt.Println("value type implements:", reflect.ValueOf(bytes.Buffer{}).Type().Implements(st))
	fmt.Println("int assignable:", reflect.ValueOf(42).Type().AssignableTo(st))
	fmt.Println("num methods:", bufT.NumMethod() > 0)
}
