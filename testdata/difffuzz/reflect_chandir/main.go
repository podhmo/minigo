package main

import (
	"fmt"
	"reflect"
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func main() {
	try(0, func() any {
		var rc <-chan int
		var sc chan<- int
		return fmt.Sprintf("%v %v %v",
			reflect.TypeOf(rc).ChanDir(),
			reflect.TypeOf(sc).ChanDir(),
			reflect.ChanOf(reflect.RecvDir, reflect.TypeOf(0)).ChanDir())
	})
}
