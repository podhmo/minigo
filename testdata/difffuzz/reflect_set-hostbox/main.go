package main

import (
	"bytes"
	"fmt"
	"reflect"
)

// Set through a pointer Elem must write INTO the tagged host box's
// pointee like Go's (*p).Set — a bare payload over the cell would drop
// the *T the type's methods live on.

type S struct {
	B bytes.Buffer
}

func main() {
	// dst = Elem(&b): Set replaces b's contents in place
	var b bytes.Buffer
	b.WriteString("x")
	dst := reflect.ValueOf(&b).Elem()
	dst.Set(reflect.ValueOf(bytes.Buffer{}))
	fmt.Println("after Set:", b.String() == "")
	b.WriteString("again")
	fmt.Println("still usable:", b.String())

	// a src read through another pointer Elem copies content; the two
	// objects never alias
	var b2 bytes.Buffer
	b2.WriteString("hi")
	dst.Set(reflect.ValueOf(&b2).Elem())
	fmt.Println("copied:", b.String())
	b2.WriteString("!")
	fmt.Println("no alias:", b.String(), b2.String())

	// the same write lands inside a struct field's box
	var s S
	s.B.WriteString("f")
	fv := reflect.ValueOf(&s).Elem().Field(0)
	fv.Set(reflect.ValueOf(bytes.Buffer{}))
	fmt.Println("field after Set:", s.B.String() == "")
	s.B.WriteString("z")
	fmt.Println("field usable:", s.B.String())

	// an any slot holding a box REPLACES on Set — not a pointee write
	var a any = &b2
	va := reflect.ValueOf(&a).Elem()
	va.Set(reflect.ValueOf(bytes.Buffer{}))
	fmt.Printf("any replaced: %T %v\n", a, b2.String())

	// unaddressable dst still panics with Go's wording
	v := reflect.ValueOf(b)
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic:", r)
		}
	}()
	v.Set(reflect.ValueOf(bytes.Buffer{}))
}
