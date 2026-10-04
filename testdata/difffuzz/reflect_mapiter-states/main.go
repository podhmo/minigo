package main

import (
	"fmt"
	"reflect"
)

func main() {
	// a nil map iterates as an already-exhausted iterator.
	var m map[string]int
	it := reflect.ValueOf(m).MapRange()
	fmt.Println("next:", it.Next())
	it.Reset(reflect.ValueOf(m))
	fmt.Println("next2:", it.Next())

	var s string
	func() {
		defer func() { fmt.Println("k:", recover()) }()
		reflect.ValueOf(&s).Elem().SetIterKey(it)
	}()

	m2 := map[string]int{"a": 1}
	it2 := reflect.ValueOf(m2).MapRange()
	func() {
		defer func() { fmt.Println("k0:", recover()) }()
		it2.Key()
	}()
	func() {
		defer func() { fmt.Println("v0:", recover()) }()
		it2.Value()
	}()
	for it2.Next() {
	}
	func() {
		defer func() { fmt.Println("ek:", recover()) }()
		it2.Key()
	}()
	func() {
		defer func() { fmt.Println("ev:", recover()) }()
		it2.Value()
	}()
	func() {
		defer func() { fmt.Println("sk:", recover()) }()
		reflect.ValueOf(&s).Elem().SetIterKey(it2)
	}()
	func() {
		defer func() { fmt.Println("sv:", recover()) }()
		reflect.ValueOf(&s).Elem().SetIterValue(it2)
	}()
}
