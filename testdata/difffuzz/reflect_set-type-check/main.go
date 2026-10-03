package main

import (
	"fmt"
	"reflect"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	n := 1
	reflect.ValueOf(&n).Elem().Set(reflect.ValueOf("bad"))
	fmt.Println(n)
}
