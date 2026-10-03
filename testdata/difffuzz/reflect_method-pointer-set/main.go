package main

import (
	"fmt"
	"reflect"
)

type S struct{}

func (*S) F() {}

type I interface{ F() }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.TypeOf(S{}).NumMethod(), reflect.TypeOf(S{}).Implements(reflect.TypeOf((*I)(nil)).Elem()))
}
