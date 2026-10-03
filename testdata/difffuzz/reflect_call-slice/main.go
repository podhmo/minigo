package main

import (
	"fmt"
	"reflect"
)

func sum(ns ...int) int {
	n := 0
	for _, v := range ns {
		n += v
	}
	return n
}
func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	out := reflect.ValueOf(sum).CallSlice([]reflect.Value{reflect.ValueOf([]int{1, 2})})
	fmt.Println(out[0].Int())
}
