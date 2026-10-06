package main

import (
	"fmt"
	"reflect"
)

func main() {
	defer func() { fmt.Println("recovered:", recover()) }()
	var e any
	t := reflect.TypeOf(e)
	fmt.Println(t.Elem())
}
