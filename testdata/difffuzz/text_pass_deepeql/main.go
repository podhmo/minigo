package main

import (
	"fmt"
	"io"
	"reflect"
)

type A []int

func main() {
	// type identity comes first: slice vs array, named vs unnamed,
	// pointer vs value — none of these share a dynamic type.
	fmt.Println(reflect.DeepEqual([]int{1}, [1]int{1}))
	fmt.Println(reflect.DeepEqual(A{1}, []int{1}))
	fmt.Println(reflect.DeepEqual([]int{}, []string{}))
	x := 1
	fmt.Println(reflect.DeepEqual(&x, x))
	fmt.Println(reflect.DeepEqual(&x, &x))
	fmt.Println(reflect.DeepEqual([]int{1}, []int{1}))
	// typed nils compare by typedef identity; an untyped or
	// empty-interface nil is just nil.
	fmt.Println(reflect.DeepEqual((*int)(nil), (*string)(nil)))
	fmt.Println(reflect.DeepEqual([]int(nil), nil))
	fmt.Println(reflect.DeepEqual([]int(nil), []int(nil)))
	fmt.Println(reflect.DeepEqual([]int(nil), []string(nil)))
	var r io.Reader
	var w io.Writer
	fmt.Println(reflect.DeepEqual(r, nil))
	fmt.Println(reflect.DeepEqual(r, w))
}
