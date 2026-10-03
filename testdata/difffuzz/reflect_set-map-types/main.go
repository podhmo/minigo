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
	m := map[string]int{}
	reflect.ValueOf(m).SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("bad"))
	fmt.Println("returned")
}
