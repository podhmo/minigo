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
	s := []byte{1}
	reflect.ValueOf(s).Bytes()[0] = 9
	fmt.Println(s[0])
}
