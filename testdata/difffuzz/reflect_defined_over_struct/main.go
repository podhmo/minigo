package main

import (
	"fmt"
	"reflect"
)

type T struct {
	A string `json:"a"`
	B int
}

type G T

func main() {
	type L T
	fmt.Println(reflect.TypeOf(G{}).NumField(), reflect.TypeOf(L{}).NumField(), reflect.TypeOf(L{}).Kind())
	l := L{A: "x", B: 2}
	fmt.Println(reflect.ValueOf(l).Field(0), reflect.ValueOf(l).NumField())
	f, ok := reflect.TypeOf(G{}).FieldByName("A")
	fmt.Println(f.Name, f.Tag.Get("json"), f.Index, ok)
	type W struct{ G }
	f, ok = reflect.TypeOf(W{}).FieldByName("B")
	fmt.Println(f.Name, f.Index, ok, reflect.TypeOf(L{}).Field(1).Type)
	t := T(l)
	fmt.Println(t.A, t.B)
}
