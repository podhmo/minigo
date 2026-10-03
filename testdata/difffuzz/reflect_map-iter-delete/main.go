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
	m := map[string]int{"a": 1}
	it := reflect.ValueOf(m).MapRange()
	delete(m, "a")
	fmt.Println(it.Next())
}
