package main

import (
	"fmt"
	"reflect"
)

// A type defined over another struct (`type U T`) has no Fields of its
// own — the field list and each field's Offset resolve through the
// underlying struct.

type LayoutT struct {
	A byte
	B int64
}

type LayoutU LayoutT

func main() {
	t := reflect.TypeOf(LayoutU{})
	fmt.Println(t.NumField())
	fmt.Println(t.Field(0).Offset, t.Field(1).Offset)
	f, ok := t.FieldByName("B")
	fmt.Println(f.Offset, ok, f.Index)
}
