package main

import (
	"fmt"
	"reflect"
)

// A declared method must not be shadowed by a promoted FIELD of the
// same name — Go's selector picks the shallowest binding, so Outer's
// own F() wins over Inner's promoted field F. Conversely a declared
// field still shadows a promoted method, and a func-typed field is a
// field, not a method.

type MInner struct{ F int }

func (MInner) G() int { return 1 }

type MOuter struct{ MInner }

func (MOuter) F() int { return 42 }

type MShadow struct {
	MInner
	G int // declared field shadows promoted method MInner.G
}

type MFunc struct {
	H func() int
}

func main() {
	v := reflect.ValueOf(MOuter{})
	m := v.MethodByName("F")
	fmt.Println(m.IsValid(), m.Call(nil)[0].Int())
	m = v.MethodByName("G")
	fmt.Println(m.IsValid(), m.Call(nil)[0].Int())
	s := reflect.ValueOf(MShadow{})
	fmt.Println(s.MethodByName("G").IsValid())
	f := reflect.ValueOf(MFunc{H: func() int { return 3 }})
	fmt.Println(f.MethodByName("H").IsValid())
}
