package main

import (
	"fmt"
	"reflect"
)

type B struct{ X int }

func (b *B) M() int { return b.X }

type S struct {
	A B
	C [2]B
	P *B
}

func main() {
	var s S
	s.A.X = 1
	s.C[1].X = 2
	rv := reflect.ValueOf(&s).Elem()
	for _, v := range []reflect.Value{rv.Field(0).Addr(), rv.Field(1).Index(1).Addr(), rv.Addr()} {
		x := v.Interface()
		fmt.Printf("%T\n", x)
		if m, ok := x.(interface{ M() int }); ok {
			fmt.Println(m.M())
		}
	}
	rv.Field(2).Set(rv.Field(0).Addr())
	fmt.Println(s.P.X, s.P == &s.A)
}
