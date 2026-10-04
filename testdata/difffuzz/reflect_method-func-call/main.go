package main

import (
	"fmt"
	"reflect"
)

type S int

func (s S) M() string { return fmt.Sprintf("val %d", s) }
func (s *S) Inc()     { *s++ }
func (s S) Add(x int) { fmt.Println(int(s) + x) }

type U struct{ S }

func main() {
	t := reflect.TypeOf(S(0))
	// (*S).M — a value-receiver member under a pointer type: Func
	// adapts the *S argument to S.
	fn, ok := reflect.PointerTo(t).MethodByName("M")
	if !ok {
		panic("no M")
	}
	fn.Func.Call([]reflect.Value{reflect.New(t)})
	// (*S).Inc — a pointer receiver binds the *S argument.
	inc, ok := reflect.PointerTo(t).MethodByName("Inc")
	if !ok {
		panic("no Inc")
	}
	nv := reflect.New(t)
	inc.Func.Call([]reflect.Value{nv})
	fmt.Println(nv.Elem().Interface())
	// S.M — the plain method expression on the value type.
	vm, ok := t.MethodByName("M")
	if !ok {
		panic("no value M")
	}
	fmt.Println(vm.Func.Call([]reflect.Value{reflect.ValueOf(S(3))})[0].Interface())
	// a promoted member re-selects on the concrete receiver.
	pm, ok := reflect.TypeOf(U{S: 4}).MethodByName("M")
	if !ok {
		panic("no promoted M")
	}
	fmt.Println(pm.Func.Call([]reflect.Value{reflect.ValueOf(U{S: 4})})[0].Interface())
}
