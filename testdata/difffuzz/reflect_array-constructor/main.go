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
	t := reflect.ArrayOf(2, reflect.TypeOf(0))
	v := reflect.Zero(t)
	fmt.Println(t.Kind(), v.Kind(), t == v.Type())
}
