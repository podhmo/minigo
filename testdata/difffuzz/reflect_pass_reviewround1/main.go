package main

import (
	"fmt"
	"reflect"
	"strings"
)

type I interface{ Len() int }

func main() {
	fmt.Println("host implements:",
		reflect.TypeOf(strings.NewReader("")).Implements(
			reflect.TypeOf((*I)(nil)).Elem()))

	fmt.Println("pointer comparable:",
		reflect.TypeOf((*[]int)(nil)).Comparable())

	fmt.Println("unsigned convert:",
		reflect.ValueOf(uint8(1)).Convert(
			reflect.TypeOf(int64(0))).Int())
}
