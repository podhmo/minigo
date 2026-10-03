package main

import (
	"fmt"
	"reflect"
)

type S[T any] struct{ X T }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.TypeOf(S[int]{}) == reflect.TypeOf(S[string]{}))
}
