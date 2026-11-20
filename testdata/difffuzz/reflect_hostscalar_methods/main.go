package main

import (
	"fmt"
	"reflect"
	"time"
)

type P *P

// a bound host scalar typedef's methods live on the host type:
// reflect.TypeOf sees NumMethod/MethodByName, and the Func calls.
func main() {
	t := reflect.TypeOf(time.Monday)
	fmt.Println(t.NumMethod(), t.Name())
	m, ok := t.MethodByName("String")
	fmt.Println(m.Name, ok)
	v := reflect.ValueOf(time.Monday)
	fmt.Println(m.Func.Call([]reflect.Value{v})[0])
	d := reflect.TypeOf(time.Second)
	fmt.Println(d.NumMethod())
	var mo time.Month = time.July
	fmt.Println(reflect.TypeOf(mo).NumMethod())
	// facade shell types must not leak internal methods, and a
	// recursive pointer typedef must not hang method probing.
	tv := reflect.TypeFor[*reflect.Value]()
	_, ok = tv.MethodByName("APIType")
	fmt.Println(ok)
	fmt.Println(reflect.TypeFor[P]().NumMethod())
}
