package main

import (
	"fmt"
	"reflect"
)

type N int

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.ValueOf(N(1)).Equal(reflect.ValueOf(N(1))))
}
