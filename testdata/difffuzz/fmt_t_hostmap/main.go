package main

import (
	"fmt"
	"reflect"
	"time"
)

func main() {
	// an unnamed composite over a named host element keeps the
	// element's package-qualified identity like reflect does.
	d := reflect.TypeOf(time.Duration(0))
	fmt.Println(reflect.MapOf(reflect.TypeOf(""), d))
	fmt.Println(reflect.ArrayOf(3, d))
	fmt.Println(reflect.SliceOf(d))
	p := reflect.TypeOf((*time.Time)(nil))
	fmt.Println(reflect.MapOf(reflect.TypeOf(""), p))
}
