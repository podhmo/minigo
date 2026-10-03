package main

import (
	"fmt"
	"reflect"
)

type S struct{ X []int }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.TypeOf(S{}).Comparable(), reflect.TypeOf([1][]int{}).Comparable())
}
