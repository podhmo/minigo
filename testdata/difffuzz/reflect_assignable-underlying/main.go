package main

import (
	"fmt"
	"reflect"
)

type S []int

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.TypeOf(S{}).AssignableTo(reflect.TypeOf([]int{})))
}
