package main

// Addr() on an addressable view whose stamped typedef was lost upstream
// crashed with a host nil-pointer panic: a field declared with a
// function-local type is invisible to the package index, so the
// []*local field's element td — and the pointee's td after Elem() —
// came back nil while the view stayed addressable. Addr() now recovers
// the type from the value like Type() does (upstream TestEscapeSet).

import (
	"fmt"
	"reflect"
)

func main() {
	type dataItem struct {
		Children []*dataItem
		X        string
	}
	data := dataItem{Children: []*dataItem{{X: "foo"}}}
	pt := reflect.ValueOf(data).Field(0).Index(0).Elem()
	fmt.Println(pt.Type(), pt.CanAddr())
	a := pt.Addr()
	fmt.Println(a.Type())
}
