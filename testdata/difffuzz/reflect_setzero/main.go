package main

import (
	"bytes"
	"fmt"
	"reflect"
)

type NS []int
type NT struct {
	A int
	B string
}

var (
	i  = 42
	s  = "hi"
	ns = NS{1, 2}
	nt = NT{A: 7, B: "x"}
	m  = map[string]int{"a": 1}
	p  = &i
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	fmt.Printf("%d: %v\n", i, f())
}

func main() {
	// scalars/aggregates zero in place
	reflect.ValueOf(&i).Elem().SetZero()
	fmt.Println(i)
	reflect.ValueOf(&s).Elem().SetZero()
	fmt.Printf("%q\n", s)
	reflect.ValueOf(&ns).Elem().SetZero()
	fmt.Printf("%v %v\n", ns, ns == nil)
	reflect.ValueOf(&nt).Elem().SetZero()
	fmt.Printf("%+v\n", nt)
	reflect.ValueOf(&m).Elem().SetZero()
	fmt.Println(m == nil)
	// host-boxed var: contents zeroed in place
	var b bytes.Buffer
	b.WriteString("abc")
	reflect.ValueOf(&b).Elem().SetZero()
	fmt.Printf("%q\n", b.String())
	// pointer var zeroes to nil
	reflect.ValueOf(&p).Elem().SetZero()
	fmt.Println(p == nil)
	// unaddressable / unsettable targets panic
	try(0, func() any {
		reflect.ValueOf(i).SetZero()
		return "no panic"
	})
	try(1, func() any {
		reflect.ValueOf(nt).Field(0).SetZero()
		return "no panic"
	})
	// zero Value
	try(2, func() any {
		reflect.Value{}.SetZero()
		return "no panic"
	})
	// interface var
	var a any = 3
	reflect.ValueOf(&a).Elem().SetZero()
	fmt.Println(a == nil)
}
