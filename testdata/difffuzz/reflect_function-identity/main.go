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
	f := func() {}
	fmt.Println(reflect.TypeOf(f) == reflect.TypeOf(f))
}
