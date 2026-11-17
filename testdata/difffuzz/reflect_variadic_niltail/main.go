package main

import (
	"fmt"
	"reflect"
)

func main() {
	impl := reflect.MakeFunc(reflect.TypeOf(func(string, ...int) string { return "" }),
		func(args []reflect.Value) []reflect.Value {
			fmt.Println("tail nil?", args[1].IsNil())
			fmt.Println("tail len:", args[1].Len())
			return []reflect.Value{reflect.ValueOf("ok")}
		})
	// The MakeFunc'd func crosses an `any` — its declared typedef is
	// lost, so Value.Call cannot read the signature off the func.
	var fn func(string, ...int) string = impl.Interface().(func(string, ...int) string)
	fn("a") // direct call: variadic tail is nil
	// Value.Call always packs the extras into a non-nil slice.
	reflect.ValueOf(fn).Call([]reflect.Value{reflect.ValueOf("a")})
	impl.Call([]reflect.Value{reflect.ValueOf("a")})
	fn("a", 1) // direct call with extras: tail is a real slice
}
