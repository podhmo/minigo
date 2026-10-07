package main

import (
	"fmt"
	"reflect"
)

// Grow's spare capacity holds element zeros: SetLen then exposes valid
// zero elements — encoding/json/v2's slice arshaler grows, SetLen(cap)s
// and decodes into va.Index(i).

type T struct{ A int }

func main() {
	var xs []any
	v := reflect.ValueOf(&xs).Elem()
	v.Grow(2)
	v.SetLen(2)
	e := v.Index(1)
	fmt.Println(e.IsValid(), e.Kind(), e.IsNil())
	e.Set(reflect.ValueOf("s"))
	fmt.Println(xs[1], xs[0] == nil)

	var ts []T
	w := reflect.ValueOf(&ts).Elem()
	w.Grow(3)
	w.SetLen(3)
	w.Index(2).Field(0).SetInt(7)
	fmt.Println(ts[0].A, ts[2].A, w.Index(1).Interface())

	ms := make([]any, 0, 4)
	m := reflect.ValueOf(&ms).Elem()
	m.SetLen(2)
	fmt.Println(m.Index(1).IsValid(), m.Index(1).IsNil())
}
