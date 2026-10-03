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
	a := [2]int{1, 2}
	v := reflect.ValueOf(&a).Elem().Slice(0, 1)
	fmt.Println(v.Kind(), v.Type() == reflect.TypeOf([]int{}))
}
