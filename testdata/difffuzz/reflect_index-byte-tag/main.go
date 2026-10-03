package main

import (
	"fmt"
	"reflect"
)

func main() {
	v1 := reflect.ValueOf("hi").Index(0)
	fmt.Printf("%T %v\n", v1.Interface(), v1.Interface())
	var us []uint8 = []uint8{104}
	v2 := reflect.ValueOf(us).Index(0)
	fmt.Printf("%T %v\n", v2.Interface(), v2.Interface())
}
