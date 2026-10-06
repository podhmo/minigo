package main

import (
	"fmt"
	"reflect"
)

func main() {
	defer func() { fmt.Println(recover()) }()
	v := reflect.Zero(reflect.TypeOf((*error)(nil)).Elem()).Elem()
	fmt.Println(v.Interface())
}
