package main

import (
	"fmt"
	"reflect"
)

type rHidden struct {
	x int
	Y int
}

func main() {
	fmt.Println(reflect.ValueOf([]int{1, 2, 3}))
	fmt.Println(reflect.ValueOf("hi"))
	fmt.Println(reflect.ValueOf(map[string]int{"a": 1}))
	fmt.Printf("%T|%d|%q|%s\n", reflect.ValueOf(7), reflect.ValueOf(7), reflect.ValueOf("hi"), reflect.ValueOf("hi"))
	fmt.Println(reflect.Value{})
	var zv reflect.Value
	fmt.Println(zv)
	fmt.Println(reflect.ValueOf(rHidden{x: 1}).Field(0))
	fmt.Println(reflect.ValueOf(reflect.ValueOf(9)))
	fmt.Println(reflect.ValueOf((*int)(nil)))
	fmt.Println(reflect.ValueOf(rHidden{x: 2}))
	fmt.Println([]any{reflect.ValueOf("s"), reflect.Value{}})
	fmt.Printf("%#v\n", reflect.ValueOf(map[string]int{"a": 1}))
	fmt.Println(reflect.TypeOf(1))
	fmt.Printf("%T|%v\n", reflect.TypeOf(1), reflect.TypeOf([]int{}))
}
