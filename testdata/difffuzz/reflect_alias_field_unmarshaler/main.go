package main

import (
	"encoding/json"
	"fmt"
	"reflect"
)

type BoolSchema struct {
	Has *bool
}

type AdditionalProperties = BoolSchema

func (b *BoolSchema) UnmarshalJSON(data []byte) error {
	var x bool
	if err := json.Unmarshal(data, &x); err != nil {
		return err
	}
	b.Has = &x
	return nil
}

type Schema struct {
	AP AdditionalProperties `json:"ap"`
}

type Unmarshaler interface{ UnmarshalJSON([]byte) error }

func main() {
	ft := reflect.TypeOf(Schema{}).Field(0).Type
	fmt.Println(ft, ft.Name(), reflect.PointerTo(ft).NumMethod(), reflect.PointerTo(ft).Implements(reflect.TypeOf((*Unmarshaler)(nil)).Elem()))
	var s0 Schema
	fv := reflect.ValueOf(&s0).Elem().Field(0)
	u, ok := reflect.TypeAssert[Unmarshaler](fv.Addr())
	fmt.Println(ok, fv.Type(), fv.Addr().Type())
	if ok {
		fmt.Println(u.UnmarshalJSON([]byte("false")), *s0.AP.Has)
	}
	_, ok = fv.Addr().Interface().(Unmarshaler)
	fmt.Println(ok)
	var s Schema
	err := json.Unmarshal([]byte(`{"ap":true}`), &s)
	fmt.Println(err, s.AP.Has != nil && *s.AP.Has)
}
