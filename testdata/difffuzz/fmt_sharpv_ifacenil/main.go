package main

import "fmt"

type S struct {
	E error
	I interface{}
}

type AnyBox struct{ A any }

type MyIface interface{ M() int }

type WithM struct{ H MyIface }

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

func main() {
	// a nil in an interface-typed field or element spells the static
	// type under %#v: error(nil), interface {}(nil), main.MyIface(nil).
	fmt.Printf("%#v\n", S{E: nil, I: nil})
	fmt.Printf("%#v\n", []interface{}{1, nil})
	fmt.Printf("%#v\n", map[string]interface{}{"a": nil, "b": 2})
	fmt.Printf("%#v\n", []error{nil})
	fmt.Printf("%#v\n", map[string]error{"x": nil})
	fmt.Printf("%#v\n", []MyIface{nil})
	fmt.Printf("%#v\n", map[interface{}]int{nil: 1})
	fmt.Printf("%#v\n", [][]interface{}{{nil}})
	fmt.Printf("%#v\n", AnyBox{A: nil})
	fmt.Printf("%#v\n", [2]interface{}{nil, 1})
	fmt.Printf("%#v\n", WithM{H: nil})
	fmt.Printf("%#v\n", &struct{ E error }{E: nil})
	fmt.Printf("%#v\n", Pair[error, any]{Key: nil, Val: nil})
	fmt.Printf("%#v\n", map[string][]error{"a": {nil}})

	// an interface holding a typed nil keeps its dynamic type.
	var c chan int
	fmt.Printf("%#v\n", []interface{}{c})

	// other verbs keep <nil> for interface nils.
	fmt.Printf("%v\n", S{E: nil, I: nil})
	fmt.Printf("%v\n", []interface{}{1, nil})
	fmt.Printf("%+v\n", S{E: nil, I: nil})
	fmt.Printf("%s\n", S{E: nil, I: nil})
	fmt.Printf("%q\n", []interface{}{1, nil})

	// top-level nil interfaces have no static type to spell.
	var e error
	var i interface{}
	fmt.Printf("%#v\n", e)
	fmt.Printf("%#v\n", i)
	fmt.Printf("%#v %T\n", e, e)
}
