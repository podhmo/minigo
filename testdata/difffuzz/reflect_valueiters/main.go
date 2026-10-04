package main

import (
	"fmt"
	"reflect"
)

type S struct {
	X int `json:"x"`
	Y string
	z bool
}

type T struct{ N int }

func (t T) Sum(a, b int) int { return t.N + a + b }
func (t T) Name() string     { return "t" }

type I interface{ Name() string }

var (
	sv  = S{X: 1, Y: "s", z: true}
	tv  = T{N: 7}
	iv  I
	ivv I = tv
)

func try(i int, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	f()
}

func main() {
	for f, x := range reflect.ValueOf(sv).Fields() {
		var payload any
		if x.CanInterface() {
			payload = x.Interface()
		} else {
			payload = "<ro>"
		}
		fmt.Println("f", f.Name, f.Type.Kind(), payload, x.Kind())
	}
	// field value is the real field — Set works through it
	pv := reflect.ValueOf(&sv).Elem()
	for f, x := range pv.Fields() {
		if f.Name == "X" {
			x.SetInt(9)
		}
	}
	fmt.Println(sv.X)
	// early stop
	n := 0
	reflect.ValueOf(sv).Fields()(func(f reflect.StructField, x reflect.Value) bool {
		n++
		return f.Name != "Y"
	})
	fmt.Println("stopped", n)
	// methods: descriptor + bound func — call through the yielded func
	for m, f := range reflect.ValueOf(tv).Methods() {
		fmt.Println("m", m.Name, m.Type)
		if f.Type().NumIn() == 2 {
			out := f.Call([]reflect.Value{reflect.ValueOf(1), reflect.ValueOf(2)})
			fmt.Println("call", out[0].Interface())
		}
	}
	// interface value's Methods sees the concrete set too (v.Method(i))
	for m := range reflect.ValueOf(ivv).Methods() {
		fmt.Println("im", m.Name)
	}
	// no methods — empty
	c := 0
	for range reflect.ValueOf(0).Methods() {
		c++
	}
	fmt.Println("int-methods", c)
	// gates
	try(0, func() { reflect.ValueOf(0).Fields() })
	try(1, func() { reflect.ValueOf(&sv).Fields() })
}
