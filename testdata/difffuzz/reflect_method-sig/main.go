package main

import (
	"fmt"
	"reflect"
	"strings"
)

var r_i0 = 42

func main() {
	fmt.Printf("%T\n", reflect.ValueOf(r_i0).Len)
	fmt.Printf("%T\n", reflect.ValueOf([]int{1}).Len)
	fmt.Printf("%T\n", strings.NewReader("x").Len)
	fmt.Printf("%T\n", reflect.ValueOf(r_i0).Interface)
}
