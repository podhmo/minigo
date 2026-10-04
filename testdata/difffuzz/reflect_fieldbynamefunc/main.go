package main

import (
	"fmt"
	"reflect"
	"strings"
)

type Inner struct{ X int }
type Other struct{ Y string }
type Outer struct {
	Inner
	Other
	B int
}
type Flat struct {
	A int
	B string
	c float64
}
type Deep struct {
	Outer
	D bool
}
type Diamond struct {
	L struct{ Inner }
	R struct{ Inner }
}
type Ambig struct {
	A int
	B int
}

var calls []string

func spy(target string) func(string) bool {
	return func(s string) bool {
		calls = append(calls, s)
		return s == target
	}
}

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	fmt.Printf("%d: %v\n", i, f())
}

func main() {
	o := Outer{B: 5}
	v := reflect.ValueOf(o)
	// direct + promoted hits
	fmt.Println(v.FieldByNameFunc(spy("B")).Interface())
	fmt.Println(v.FieldByNameFunc(spy("X")).Interface())
	fmt.Println(v.FieldByNameFunc(spy("Y")).Interface())
	// miss → invalid Value
	miss := v.FieldByNameFunc(spy("ZZ"))
	fmt.Println(miss.IsValid())
	// promoted two levels deep
	d := Deep{Outer: Outer{Inner: Inner{X: 9}}}
	fmt.Println(reflect.ValueOf(d).FieldByNameFunc(spy("X")).Interface())
	// ambiguous same-depth match annihilates
	fmt.Println(reflect.ValueOf(Ambig{}).FieldByNameFunc(func(s string) bool { return strings.HasPrefix(s, "") }).IsValid())
	// diamond: same struct type reached twice at a level
	dm := Diamond{}
	dm.L.Inner.X = 1
	dm.R.Inner.X = 2
	fmt.Println(reflect.ValueOf(dm).FieldByNameFunc(spy("X")).IsValid())
	// call order is observable: BFS, every field name, embedded included
	calls = nil
	reflect.ValueOf(o).FieldByNameFunc(spy("X"))
	fmt.Println(calls)
	// Type-level method
	f, ok := reflect.TypeOf(o).FieldByNameFunc(spy("X"))
	fmt.Println(f.Name, f.Index, ok)
	_ = f
	// non-struct panics via the Type-level message
	try(0, func() any { return reflect.ValueOf(3).FieldByNameFunc(spy("A")) })
	try(1, func() any {
		_, e := reflect.TypeOf(3).FieldByNameFunc(spy("A"))
		return e
	})
	// zero Value
	try(2, func() any { return reflect.Value{}.FieldByNameFunc(spy("A")) })
}
