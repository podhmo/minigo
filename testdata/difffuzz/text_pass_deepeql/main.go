package main

import (
	"fmt"
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
}
