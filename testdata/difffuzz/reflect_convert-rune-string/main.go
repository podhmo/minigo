package main

import (
	"fmt"
	"reflect"
)

type R rune

func main() {
	// a rune element type converts to/from UTF-8 contents, not raw bytes
	fmt.Println(reflect.ValueOf([]rune("あい")).Convert(reflect.TypeOf("")).Interface())
	fmt.Println(reflect.ValueOf([]R("あい")).Convert(reflect.TypeOf("")).Interface())
	fmt.Println(reflect.ValueOf("あい").Convert(reflect.TypeOf([]rune{})).Interface())
	// undecodable bytes become RuneError; out-of-range runes print as U+FFFD
	fmt.Println(reflect.ValueOf("a\xffb").Convert(reflect.TypeOf([]rune{})).Interface())
	fmt.Println(reflect.ValueOf([]rune{-1, 'z'}).Convert(reflect.TypeOf("")).Interface())
}
