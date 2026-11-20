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
}
