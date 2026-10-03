package main

import (
	"fmt"
	"reflect"
)

func main() {
	var s []int
	var m map[string]int
	var ch chan int
	sv := reflect.ValueOf(s)
	mv := reflect.ValueOf(m)
	cv := reflect.ValueOf(ch)
	fmt.Println(sv.Len(), sv.Cap(), mv.Len(), cv.Len(), cv.Cap())
	fmt.Println(len(mv.MapKeys()), mv.MapIndex(reflect.ValueOf("k")).IsValid())
}
