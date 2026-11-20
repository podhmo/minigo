package main

import (
	"errors"
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
	_, ok = tv.MethodByName("IsValid")
	fmt.Println(ok)
	mi := reflect.TypeFor[*reflect.MapIter]()
	_, ok = mi.MethodByName("Next")
	fmt.Println(mi.NumMethod(), ok)
	m, _ = mi.MethodByName("Key")
	fmt.Println(m.Index, m.Type)
	fmt.Println(mi.Method(m.Index).Name)
	et := reflect.TypeOf(errors.New).Out(0)
	fmt.Println(et.Method(9).Name == "")
	// value-type method sets report the gc set — MapIter has 0,
	// StructField's IsExported is the value-receiver exception.
	fmt.Println(reflect.TypeFor[reflect.MapIter]().NumMethod())
	fmt.Println(reflect.TypeFor[reflect.StructField]().NumMethod())
	mv, _ := reflect.TypeFor[reflect.Value]().MethodByName("IsValid")
	fmt.Println(mv.Type)
	// interface requirements: zero Func, receiver-free Type.
	ms, _ := reflect.TypeFor[reflect.Type]().MethodByName("String")
	fmt.Println(ms.Func.IsValid(), ms.Type)
	fmt.Println(reflect.TypeFor[P]().NumMethod())
}
