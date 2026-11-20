package main

import (
	"fmt"
	"reflect"
)

func main() {
	x := reflect.ValueOf("hi").Convert(reflect.TypeOf([]byte(nil))).Interface()
	fmt.Println(reflect.DeepEqual(x, []byte("hi")), reflect.TypeOf(x))
}
