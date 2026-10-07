package main

import (
	"fmt"
	"reflect"
)

var reflectValueType = reflect.TypeFor[reflect.Value]()

func length(item reflect.Value) (int, error) {
	fmt.Println("kind", item.Kind())
	for ; item.Kind() == reflect.Pointer || item.Kind() == reflect.Interface; item = item.Elem() {
		if item.IsNil() {
			return 0, fmt.Errorf("nil")
		}
	}
	return item.Len(), nil
}

type S struct{ P []string }

func main() {
	s := &S{P: []string{"a", "b"}}
	field := reflect.ValueOf(s).Elem().Field(0)
	fn := reflect.ValueOf(length)
	typ := fn.Type().In(0)
	fmt.Println(typ == reflectValueType)
	arg := field
	if typ == reflectValueType && arg.Type() != typ {
		arg = reflect.ValueOf(arg)
	}
	out := fn.Call([]reflect.Value{arg})
	fmt.Println(out[0].Interface(), out[1].Interface())
	back := arg.Interface().(reflect.Value)
	fmt.Println(back.Kind(), back.Len())
}
