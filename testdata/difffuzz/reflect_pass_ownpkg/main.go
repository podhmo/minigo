package main

import (
	"fmt"
	"reflect"
)

type Box struct {
	Inner *Box
}

func main() {
	dst := reflect.New(reflect.TypeOf(Box{})).Elem()
	f := dst.Field(0)
	cp := reflect.New(f.Type().Elem())
	f.Set(cp)
	fmt.Println("set:", f.Interface() == cp.Interface())
	fmt.Println(reflect.TypeOf(Box{}).Field(0).Type)
}
