package main

import (
	"fmt"
	"reflect"
)

func main() {
	v := reflect.ValueOf(42)
	vv := reflect.ValueOf(v)
	// methods of reflect.Value itself, invoked through reflection on
	// a Value-of-Value — the receiver satisfies In(0)=reflect.Value
	// via the facade's API type, not a marshalable host value.
	fmt.Println(vv.MethodByName("IsValid").Call(nil)[0].Bool())
	fmt.Println(vv.MethodByName("CanInterface").Call(nil)[0].Bool())
	fmt.Println(vv.MethodByName("CanSet").Call(nil)[0].Bool())
	// the bound method operates on the boxed value, not the box: Int
	// reads 42 through the Value-of-Value, and IsValid on a box of a
	// zero Value answers false.
	fmt.Println(vv.MethodByName("Int").Call(nil)[0].Int())
	var z reflect.Value
	vz := reflect.ValueOf(z)
	fmt.Println(vz.MethodByName("IsValid").Call(nil)[0].Bool())
}
