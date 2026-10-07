package main

import (
	"fmt"
	"reflect"
)

var x = struct{ a, _, c int }{1, 2, 3}

func main() {
	v := reflect.ValueOf(x)
	for i := 0; i < v.NumField(); i++ {
		fmt.Println(v.Field(i).Int())
	}
}
