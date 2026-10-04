package main

import (
	"fmt"
	"reflect"
	"strings"
)

// MakeFunc validates the callback's outputs against the produced
// func's signature when the call returns — the panic is caught and
// its message checked inside the program so output stays printable.

func mk(t reflect.Type, outs []reflect.Value) (r any) {
	f := reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value { return outs })
	defer func() { r = recover() }()
	f.Call(nil)
	return nil
}

func main() {
	two := reflect.TypeOf((func() (int, int))(nil))
	one := reflect.TypeOf((func() int)(nil))

	// too few outs
	r := mk(two, []reflect.Value{reflect.ValueOf(1)})
	fmt.Println(strings.Contains(fmt.Sprint(r), "reflect: wrong return count from function created by MakeFunc"))

	// too many outs
	r = mk(one, []reflect.Value{reflect.ValueOf(1), reflect.ValueOf(2)})
	fmt.Println(strings.Contains(fmt.Sprint(r), "reflect: wrong return count from function created by MakeFunc"))

	// `return nil` on a 1-out signature
	f := reflect.MakeFunc(one, func(args []reflect.Value) []reflect.Value { return nil })
	func() {
		defer func() { r = recover() }()
		f.Call(nil)
	}()
	fmt.Println(strings.Contains(fmt.Sprint(r), "reflect: wrong return count from function created by MakeFunc"))

	// out not assignable to the declared type (convertible is not enough)
	r = mk(one, []reflect.Value{reflect.ValueOf("str")})
	fmt.Println(strings.Contains(fmt.Sprint(r), "reflect.MakeFunc: value of type string is not assignable to type int"))

	// matching outs still call through
	ok := reflect.MakeFunc(two, func(args []reflect.Value) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(3), reflect.ValueOf(4)}
	})
	fmt.Println(ok.Call(nil))
}
