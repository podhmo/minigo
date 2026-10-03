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
	v := reflect.ValueOf(1)
	fmt.Println(reflect.ValueOf(v).Kind())
}
