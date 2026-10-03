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
	p := &n
	fmt.Println(reflect.Indirect(reflect.ValueOf(&p)).Kind())
}
