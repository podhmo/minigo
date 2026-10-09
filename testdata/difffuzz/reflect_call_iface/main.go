package main

// reflect.Value.Call results carry the DECLARED result type like Go:
// a func returning `error` yields an Interface-kind Value even when the
// payload is a struct, so IsNil answers (was: trap "call of
// reflect.Value.IsNil on struct Value").

import (
	"fmt"
	"reflect"
)

type myErr struct{}

func (myErr) Error() string { return "myError" }

func f() error { return myErr{} }

type S struct{ X int }

func g() (any, S) { return nil, S{X: 3} }

func main() {
	outs := reflect.ValueOf(f).Call(nil)
	fmt.Println(outs[0].Kind(), outs[0].Type(), outs[0].IsNil())
	fmt.Println(outs[0].Interface().(error).Error())
	g1 := reflect.ValueOf(g).Call(nil)
	fmt.Println(g1[0].Kind(), g1[0].IsNil())
	fmt.Println(g1[1].Kind(), g1[1].Interface())
}
