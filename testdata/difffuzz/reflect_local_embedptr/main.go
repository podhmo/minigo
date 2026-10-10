package main

import (
	"fmt"
	"reflect"
)

type A struct{ S string }
type B struct{ *A }

func main() {
	// package-level embedded pointer promotes S — the baseline that
	// already worked.
	f, ok := reflect.TypeOf(B{}).FieldByName("S")
	fmt.Println(f.Name, ok, f.Index)

	// function-local embedded pointer: the field type's *A2 must
	// resolve A2 through the decl-time local-type snapshot — package
	// indexes never see function scopes.
	type A2 struct{ S string }
	type B2 struct{ *A2 }
	f2, ok2 := reflect.TypeOf(B2{}).FieldByName("S")
	fmt.Println(f2.Name, ok2, f2.Index)

	// FieldByNameFunc walks the same promotion chain.
	f3, ok3 := reflect.TypeOf(B2{}).FieldByNameFunc(func(n string) bool { return n == "S" })
	fmt.Println(f3.Name, ok3)

	// a nested composite field type sees the local decl too.
	type A4 struct{ S string }
	type B4 struct{ P *A4 }
	f4 := reflect.TypeOf(B4{}).Field(0)
	fmt.Println(f4.Type.Elem().Field(0).Name)

	// promotion through a deeper local embed chain.
	type C2 struct{ *B2 }
	f5, ok5 := reflect.TypeOf(C2{}).FieldByName("S")
	fmt.Println(f5.Name, ok5, f5.Index)
}
