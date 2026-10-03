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
	v := reflect.MakeSlice(reflect.TypeOf([]int{}), 1, 4)
	fmt.Println(v.Len(), v.Cap())
}
