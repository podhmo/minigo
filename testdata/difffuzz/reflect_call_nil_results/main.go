package main

import (
	"fmt"
	"reflect"
)

var reflectValueType = reflect.TypeFor[reflect.Value]()

func indirect(v reflect.Value) (rv reflect.Value, isNil bool) {
	for ; v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface; v = v.Elem() {
		if v.IsNil() {
			return v, true
		}
	}
	return v, false
}

func length(item reflect.Value) (int, error) {
	item, isNil := indirect(item)
	if isNil {
		return 0, fmt.Errorf("len of nil pointer")
	}
	return item.Len(), nil
}

type P struct{ Name string }
type C struct{ Providers []P }

func call(fn reflect.Value, field reflect.Value) {
	arg := field
	if fn.Type().In(0) == reflectValueType && arg.Type() != reflectValueType {
		arg = reflect.ValueOf(arg)
	}
	out := fn.Call([]reflect.Value{arg})
	fmt.Println(out[0].Interface(), out[1].Interface())
}

func main() {
	fn := reflect.ValueOf(length)
	var dot any = C{}
	d := reflect.ValueOf(dot)
	call(fn, d.FieldByName("Providers"))
	call(fn, d.FieldByIndex([]int{0}))
	f, _ := d.Type().FieldByName("Providers")
	call(fn, d.FieldByIndex(f.Index))
	var args []reflect.Value
	args = append(args, reflect.ValueOf(d.Field(0)))
	out := fn.Call(args)
	fmt.Println(out[0].Interface(), out[1].Interface())
}

func init() {
	out := reflect.ValueOf(length).Call([]reflect.Value{reflect.ValueOf(reflect.ValueOf([]int{1}))})
	fmt.Println(out[1].IsValid(), out[1].Kind(), out[1].IsNil(), out[1].Type())
	f := func() (any, *int, []int, error) { return nil, nil, nil, nil }
	for _, o := range reflect.ValueOf(f).Call(nil) {
		fmt.Println(o.IsValid(), o.Kind(), o.IsNil(), o.Type())
	}
}
