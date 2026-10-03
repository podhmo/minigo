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
	var n int8
	reflect.ValueOf(&n).Elem().SetInt(257)
	fmt.Println(n)
}
