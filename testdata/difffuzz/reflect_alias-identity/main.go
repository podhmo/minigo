package main

import (
	"fmt"
	"reflect"
)

type A = int

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.TypeFor[A]() == reflect.TypeFor[int](), reflect.TypeFor[byte]() == reflect.TypeFor[uint8]())
}
