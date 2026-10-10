package main

import (
	"fmt"
	"reflect"
)

// `any` and `interface{}` are the same type: a `*any` value assigns
// to a `*interface{}` field, and reflect names the elem of either "".
type T struct{ A *interface{} }

func main() {
	v := 1
	var a = any(v)
	t := T{A: &a}
	fmt.Println(*t.A)
	var i interface{} = 2
	var u struct{ A *any }
	u.A = &i
	fmt.Println(*u.A)
	fmt.Println(reflect.TypeOf(&a).Elem().Name(), reflect.TypeOf(&a).Elem().String())
}
