package main

import (
	"fmt"
	"reflect"
)

func a() reflect.Type { type X int; return reflect.TypeOf(X(0)) }
func b() reflect.Type { type X int; return reflect.TypeOf(X(0)) }
func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(a() == b())
}
