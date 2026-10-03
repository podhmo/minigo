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
	var x uint8
	reflect.ValueOf(&x).Elem().SetUint(257)
	fmt.Println(x)
}
