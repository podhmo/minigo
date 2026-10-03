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
	v := reflect.ValueOf([1]int{1}).Index(0)
	fmt.Println(v.CanAddr(), v.CanSet())
}
