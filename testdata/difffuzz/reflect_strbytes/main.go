package main

import (
	"fmt"
	"reflect"
)

type Octet byte
type R rune

func main() {
	x := reflect.ValueOf("hi").Convert(reflect.TypeOf([]Octet(nil))).Interface()
	var o Octet = x.([]Octet)[0]
	fmt.Println(o, x.([]Octet))
	y := reflect.ValueOf("hé").Convert(reflect.TypeOf([]R(nil))).Interface()
	var r R = y.([]R)[0]
	fmt.Println(r, y.([]R))
	// still-true DeepEqual on plain []byte
	z := reflect.ValueOf("hi").Convert(reflect.TypeOf([]byte(nil))).Interface()
	fmt.Println(reflect.DeepEqual(z, []byte("hi")))
}
