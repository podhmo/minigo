package main

import (
	"fmt"
	"reflect"
)

func main() {
	var a any = reflect.ValueOf(3)
	v, ok := a.(reflect.Value)
	fmt.Println(v.Int(), ok)
}
