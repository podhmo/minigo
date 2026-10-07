package main

import (
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/testdata/difffuzz/reflect_field_pkgname_differs/inner.v2"
)

type Action struct {
	Update inner.Node
	Ptr    *inner.Node
	List   []inner.Node
}

var nodeType = reflect.TypeOf(inner.Node{})

func main() {
	var a Action
	rt := reflect.TypeOf(a)
	f := rt.Field(0).Type
	fmt.Println(f == nodeType, f.Kind(), f.PkgPath(), f.Name(), f.NumField())
	fmt.Println(rt.Field(1).Type.Elem() == nodeType, rt.Field(2).Type.Elem() == nodeType)
	rv := reflect.ValueOf(&a).Elem()
	rv.Field(0).Set(reflect.ValueOf(inner.Node{Kind: 4}))
	fmt.Println(a.Update.Kind, rv.Field(0).Type() == nodeType)
}
