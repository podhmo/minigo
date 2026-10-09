package main

import (
	"bytes"
	"fmt"
	"reflect"
)

// Elem() on an interface holding a typed nil must expose the typed
// nil with its kind intact — typeOfValue used to return nil for an
// IfaceNil, so the result's Kind() read Invalid and callers like
// text/template's isTrue could not classify it ("if/with can't use
// <nil>" where gc reports the payload's nilness).
func main() {
	var b *bytes.Buffer
	var s fmt.Stringer = b
	v := reflect.ValueOf(&s).Elem()
	fmt.Println("iface nil:", v.IsNil())
	e := v.Elem()
	fmt.Println("elem kind:", e.Kind())
	fmt.Println("elem nil:", e.IsNil())
	fmt.Println("elem type:", e.Type())

	var ni fmt.Stringer // nil interface: Elem is the zero Value
	nv := reflect.ValueOf(&ni).Elem()
	fmt.Println("nil iface nil:", nv.IsNil())
	fmt.Println("nil elem valid:", nv.Elem().IsValid())
}
