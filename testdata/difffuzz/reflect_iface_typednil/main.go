package main

// reflect.Value.IsNil on an interface-typed Value asks about the
// interface itself: an interface holding a typed nil (*T)(nil) is a
// NON-nil interface — IsNil answers false like Go (was: true, which
// made text/template's indirect() error with "nil pointer evaluating").

import (
	"fmt"
	"reflect"
)

type T struct{}

type I interface{ M() }

func (t *T) M() {}

var i I = (*T)(nil)
var j I = nil

func main() {
	fmt.Println(reflect.ValueOf(&i).Elem().IsNil())
	fmt.Println(reflect.ValueOf(&j).Elem().IsNil())
	var a any = (*T)(nil)
	fmt.Println(reflect.ValueOf(&a).Elem().IsNil())
	var b any = nil
	fmt.Println(reflect.ValueOf(&b).Elem().IsNil())
}
