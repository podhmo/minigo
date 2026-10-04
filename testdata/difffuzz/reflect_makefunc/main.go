package main

import (
	"fmt"
	"reflect"
)

func add(args []reflect.Value) []reflect.Value {
	sum := args[0].Interface().(int) + args[1].Interface().(int)
	return []reflect.Value{reflect.ValueOf(sum)}
}

func main() {
	f := reflect.MakeFunc(reflect.TypeOf((func(int, int) int)(nil)), add).Interface().(func(int, int) int)
	fmt.Println(f(2, 3))

	var called bool
	v := reflect.MakeFunc(reflect.TypeOf((func())(nil)), func(args []reflect.Value) []reflect.Value {
		called = true
		return nil
	}).Interface().(func())
	v()
	fmt.Println(called)

	// a made-func passed back as a []reflect.Value arg
	inner := reflect.MakeFunc(reflect.TypeOf((func())(nil)), func(args []reflect.Value) []reflect.Value {
		fmt.Println("inner")
		return nil
	}).Interface().(func())
	outer := reflect.MakeFunc(reflect.TypeOf((func(func()))(nil)), func(args []reflect.Value) []reflect.Value {
		args[0].Interface().(func())()
		return nil
	}).Interface().(func(func()))
	outer(inner)
}
