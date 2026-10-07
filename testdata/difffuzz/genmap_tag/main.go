package main

import "fmt"

type T[_ any] struct{}

var m = map[interface{}]int{
	T[struct{ int }]{}: 0,
	T[struct {
		int "x"
	}]{}: 0,
	T[struct{ int }]{}: 0,
}

func main() {
	fmt.Println(len(m))
}
