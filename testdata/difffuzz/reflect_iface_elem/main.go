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

	// TypeAssert on an interface-held typed nil yields the payload
	// typed as T — the raw IfaceNil box must not leak (p == nil
	// holds and the result binds to *bytes.Buffer).
	p, ok := reflect.TypeAssert[*bytes.Buffer](v)
	var q *bytes.Buffer = p
	fmt.Println("assert ptr:", ok, p == nil, q == nil)
	_, ok = reflect.TypeAssert[fmt.Stringer](nv)
	fmt.Println("assert on nil iface:", ok)

	// accessors on an Elem()'d typed nil dispatch on the TypedNil
	// shape, not the raw IfaceNil box.
	var a any = []int(nil)
	fmt.Println("slice len:", reflect.ValueOf(&a).Elem().Elem().Len())
	var m any = map[string]int(nil)
	fmt.Println("map len:", reflect.ValueOf(&m).Elem().Elem().Len())
}
