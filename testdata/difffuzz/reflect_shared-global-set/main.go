package main

import (
	"fmt"
	"reflect"
)

type RTag struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}

var (
	r_struct = RTag{A: 7, B: "t"}
	r_bytes  = []byte("abc")
	r_sptr   = &r_struct
	r_i0     = 42
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func main() {
	// probe 0 tries to blast a []byte into the shared struct global —
	// Go refuses at the assignability gate; nothing is corrupted.
	try(0, func() any {
		reflect.ValueOf(&r_struct).Elem().Set(reflect.ValueOf(r_bytes))
		return nil
	})
	// probe 1 then reads the same global — must still be RTag.
	try(1, func() any {
		return reflect.TypeOf(r_struct).Field(0).Type
	})
	try(2, func() any {
		return reflect.ValueOf(r_sptr).Elem().Type().Kind()
	})
	// write through the pointer alias, then read the struct again.
	try(3, func() any {
		reflect.ValueOf(r_sptr).Elem().Set(reflect.ValueOf(42))
		return nil
	})
	try(4, func() any {
		return reflect.ValueOf(r_sptr).Elem().Type().Kind()
	})
	// a legal same-type Set does corrupt the visible value — by design.
	try(5, func() any {
		reflect.ValueOf(&r_struct).Elem().Set(reflect.ValueOf(RTag{A: 9}))
		return "assigned"
	})
	try(6, func() any {
		return reflect.ValueOf(r_struct).Field(0).Interface()
	})
}
