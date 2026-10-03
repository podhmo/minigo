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
	fmt.Println(reflect.TypeOf([]int{}) == reflect.SliceOf(reflect.TypeOf(0)))
}
