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
	s := make([]int, 1, 4)
	v := reflect.Append(reflect.ValueOf(s), reflect.ValueOf(2))
	v.Index(0).SetInt(9)
	fmt.Println(s[0])
}
