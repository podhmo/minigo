package main

import (
	"fmt"
	"reflect"
)

type Types []string

func (types *Types) Slice() []string {
	if types == nil {
		return nil
	}
	return *types
}

func (types *Types) Set(v []string) { *types = v }

type Setter interface{ Set([]string) }

type Schema struct {
	Type *Types
}

func main() {
	p := reflect.New(reflect.TypeOf(Types{}))
	p.Interface().(Setter).Set([]string{"string"})
	fmt.Println(p.Interface().(*Types).Slice(), p.Elem().Type())
	var s Schema
	f := reflect.ValueOf(&s).Elem().Field(0)
	f.Set(reflect.New(f.Type().Elem()))
	f.Interface().(Setter).Set([]string{"a", "b"})
	fmt.Println(s.Type.Slice(), len(*s.Type))
}
