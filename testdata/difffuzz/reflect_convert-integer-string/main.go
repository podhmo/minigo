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
	v := reflect.ValueOf(65).Convert(reflect.TypeOf(""))
	fmt.Println(v.String())
}
