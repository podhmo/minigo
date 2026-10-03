package main

import (
	"fmt"
	"reflect"
)

type S struct{}

func (S) F(int) {}
func main()     { m := reflect.TypeOf(S{}).Method(0); fmt.Println(m.Type == nil, m.PkgPath == "") }
