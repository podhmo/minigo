package main

import (
	"fmt"
	"reflect"
)

type A struct {
	B int8
	C int64
}
type D struct {
	A int8
	B int8
	C int32
}
type Inner struct {
	P int8
	Q int64
}
type Outer struct {
	Inner
	Y int8
}
type OuterP struct {
	*Inner
	Y int8
}
type Mix struct {
	S string
	F []int
	I any
	P *int
}

func main() {
	t := reflect.TypeOf(A{})
	fmt.Println("A.B", t.Field(0).Offset, "A.C", t.Field(1).Offset)
	d := reflect.TypeOf(D{})
	fmt.Println("D.C", d.Field(2).Offset)
	o := reflect.TypeOf(Outer{})
	f, ok := o.FieldByName("Q")
	fmt.Println("Outer.Q", f.Offset, ok, f.Index)
	f, _ = o.FieldByName("Y")
	fmt.Println("Outer.Y", f.Offset)
	op := reflect.TypeOf(OuterP{})
	fp, _ := op.FieldByName("Q")
	fmt.Println("OuterP.Q", fp.Offset, fp.Index)
	m := reflect.TypeOf(Mix{})
	for i := 0; i < m.NumField(); i++ {
		fmt.Println(m.Field(i).Name, m.Field(i).Offset)
	}
}
