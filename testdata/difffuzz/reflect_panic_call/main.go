package main

import (
	"fmt"
	"reflect"
)

func doPanic() string { panic("custom panic string") }

func try() (s string) {
	defer func() {
		if r := recover(); r != nil {
			s = fmt.Sprint(r)
		}
	}()
	reflect.ValueOf(doPanic).Call(nil)
	return "no panic"
}

func main() {
	fmt.Println(try())
}
