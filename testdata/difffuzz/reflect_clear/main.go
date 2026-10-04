package main

import (
	"fmt"
	"reflect"
)

type NS []int
type NM map[string]int
type P struct{ X, Y int }

var (
	sl    = []int{1, 2, 3}
	nsl   = NS{4, 5}
	m     = map[string]int{"a": 1, "b": 2}
	nm    = NM{"x": 9}
	ps    = []P{{1, 2}, {3, 4}}
	str   = "s"
	st    = struct{ X int }{X: 1}
	hid   = struct{ x []int }{x: []int{1, 2}}
	hidm  = struct{ y map[string]int }{y: map[string]int{"a": 1}}
	nilsl []int
	nilm  map[string]int
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
	v := reflect.ValueOf(sl)
	fmt.Println(v.CanSet())
	v.Clear()
	fmt.Println(sl)
	reflect.ValueOf(nsl).Clear()
	fmt.Println(nsl)
	reflect.ValueOf(m).Clear()
	fmt.Println(len(m))
	reflect.ValueOf(nm).Clear()
	fmt.Println(len(nm))
	// struct elems zero independently — mutate one after Clear
	pv := reflect.ValueOf(ps)
	pv.Clear()
	pv.Index(0).Field(0).SetInt(9)
	fmt.Println(ps[1].X)
	// nil slice/map — no-op
	reflect.ValueOf(nilsl).Clear()
	reflect.ValueOf(nilm).Clear()
	fmt.Println(nilsl == nil, nilm == nil)
	// bad kinds
	try(0, func() any { reflect.ValueOf(str).Clear(); return "ok" })
	try(1, func() any { reflect.ValueOf(st).Clear(); return "ok" })
	try(2, func() any { reflect.ValueOf(3).Clear(); return "ok" })
	try(3, func() any { reflect.Value{}.Clear(); return "ok" })
	try(4, func() any { reflect.ValueOf(st).Field(0).Clear(); return "ok" })
	// unexported-field values clear anyway — no ro gate
	fv := reflect.ValueOf(&hid).Elem().Field(0)
	try(5, func() any { fv.Clear(); return fmt.Sprintf("%v", hid.x) })
	fv2 := reflect.ValueOf(&hidm).Elem().Field(0)
	try(6, func() any { fv2.Clear(); return fmt.Sprintf("%v", hidm.y) })
}
