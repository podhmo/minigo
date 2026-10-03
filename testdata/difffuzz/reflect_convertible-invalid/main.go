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
	fmt.Println(reflect.TypeOf(1).ConvertibleTo(reflect.TypeOf(complex(1, 2))), reflect.TypeOf("").ConvertibleTo(reflect.TypeOf([]int{})))
}
