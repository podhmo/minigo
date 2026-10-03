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
	fmt.Println(reflect.ValueOf([2]int{}).IsZero(), reflect.ValueOf([]int{}).IsZero(), reflect.ValueOf(map[string]int{}).IsZero())
}
