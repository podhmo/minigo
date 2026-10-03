package main

import (
	"fmt"
	"reflect"
)

type NS []int
type NA [3]int
type NI int

func main() {
	ns := NS{7, 8, 9}
	na := NA{1, 2, 3}
	ni := NI(5)
	v := reflect.ValueOf(&ns).Elem()
	fmt.Println(v.Len(), v.Cap(), v.Index(1).Interface())
	fmt.Println(reflect.ValueOf(&na).Elem().Len(), reflect.ValueOf(&na).Elem().Index(0).Interface())
	fmt.Println(reflect.ValueOf(ni).Kind())
	v.Index(0).SetInt(42)
	fmt.Println(ns)
}
