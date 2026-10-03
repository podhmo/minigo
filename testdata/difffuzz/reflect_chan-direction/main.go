package main

import (
	"fmt"
	"reflect"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	t := reflect.TypeOf(0)
	fmt.Println(reflect.ChanOf(reflect.RecvDir, t) == reflect.ChanOf(reflect.SendDir, t))
}
