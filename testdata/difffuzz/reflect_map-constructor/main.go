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
	t := reflect.MapOf(reflect.TypeOf(""), reflect.TypeOf(0))
	v := reflect.MakeMap(t)
	fmt.Println(t == v.Type(), v.Type().Key().Kind())
}
