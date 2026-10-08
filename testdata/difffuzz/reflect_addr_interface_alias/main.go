package main

import (
	"fmt"
	"reflect"
)

type BoolSchema struct{ Has *bool }

type AP = BoolSchema

func (b *BoolSchema) UnmarshalJSON(data []byte) error { return nil }

type Unmarshaler interface{ UnmarshalJSON([]byte) error }

type Schema struct{ A AP }

func main() {
	var s Schema
	fv := reflect.ValueOf(&s).Elem().Field(0)
	_, ok := fv.Addr().Interface().(Unmarshaler)
	_, ok2 := any(&s.A).(Unmarshaler)
	fmt.Println(ok, ok2)
	fmt.Printf("%T %T\n", fv.Addr().Interface(), &s.A)
	type S2 interface{ UnmarshalJSON([]byte) error }
	_, ok3 := fv.Addr().Interface().(interface{ UnmarshalJSON([]byte) error })
	fmt.Println(ok3)
}
