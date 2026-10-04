package main

import (
	"fmt"
	"reflect"
)

type S struct {
	X int    `json:"x"`
	Y string `tag:"y"`
	z bool
}

type I interface {
	Do(int) string
}

type T struct{}

func (T) Do(int) string   { return "x" }
func (T) DoMore(a, b int) {}
func (T) hidden()         {}

var (
	sv S
	fv func(int, string) (int, error)
	iv I
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
	st := reflect.TypeOf(sv)
	for f := range st.Fields() {
		fmt.Println("f", f.Name, f.Type.Kind(), f.Tag, f.Anonymous, f.Index, f.PkgPath != "")
	}
	// direct call + early stop
	n := 0
	st.Fields()(func(f reflect.StructField) bool {
		n++
		return f.Name != "Y"
	})
	fmt.Println("fields-stopped", n)
	// methods on struct type — exported only
	for m := range reflect.TypeOf(T{}).Methods() {
		fmt.Println("m", m.Name, m.Type, m.Index)
	}
	// interface methods
	for m := range reflect.TypeOf(&iv).Elem().Methods() {
		fmt.Println("im", m.Name, m.Type)
	}
	// int has no methods — empty iter, no panic
	c := 0
	for range reflect.TypeOf(0).Methods() {
		c++
	}
	fmt.Println("int-methods", c)
	// func params/results
	ft := reflect.TypeOf(fv)
	for t := range ft.Ins() {
		fmt.Println("in", t.Kind())
	}
	for t := range ft.Outs() {
		fmt.Println("out", t.Kind(), t.Name())
	}
	// gates
	try(0, func() { reflect.TypeOf(0).Fields() })
	try(1, func() { reflect.TypeOf(sv).Ins() })
	try(2, func() { reflect.TypeOf(sv).Outs() })
}
