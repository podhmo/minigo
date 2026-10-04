package main

import (
	"bytes"
	"fmt"
	"reflect"
	"time"
)

func try(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v\n", name, r)
		}
	}()
	f()
	fmt.Printf("%s: ok\n", name)
}

func main() {
	var b bytes.Buffer
	v := reflect.ValueOf(b)
	fmt.Println("ValueOf(b): Kind", v.Kind(), "Type", v.Type(),
		"CanSet", v.CanSet(), "CanAddr", v.CanAddr(), "CanInterface", v.CanInterface())

	// the pointer view stays addressable, like Go
	vp := reflect.ValueOf(&b).Elem()
	fmt.Println("Elem(&b): CanSet", vp.CanSet(), "CanAddr", vp.CanAddr())

	// writes through the copied Value must panic
	try("set-unaddr", func() {
		v.Set(reflect.ValueOf(bytes.Buffer{}))
	})
	try("addr-unaddr", func() {
		v.Addr()
	})
	try("elem-struct", func() {
		v.Elem()
	})
	try("setbytes-unaddr", func() {
		v.SetBytes([]byte("x"))
	})

	// a field read on the copied struct is unexported-flagged, never settable
	f0 := v.Field(0)
	fmt.Println("Field(0): CanSet", f0.CanSet(), "CanAddr", f0.CanAddr(), "CanInterface", f0.CanInterface())

	// a value carried by an interface is the same copy
	var i any = b
	vi := reflect.ValueOf(i)
	fmt.Println("iface ValueOf: CanSet", vi.CanSet(), "CanAddr", vi.CanAddr())

	// composite literals box the same way
	vl := reflect.ValueOf(bytes.Buffer{})
	fmt.Println("literal ValueOf: CanSet", vl.CanSet(), "CanAddr", vl.CanAddr())

	// Interface hands back a typed copy that formats like the value
	fmt.Printf("Interface: %T\n", v.Interface())

	// value-receiver host methods still call through the copy
	var t time.Time
	vt := reflect.ValueOf(t)
	fmt.Println("ValueOf(t): CanSet", vt.CanSet(), "CanAddr", vt.CanAddr())
	m := vt.MethodByName("String")
	fmt.Println("MethodByName(String): valid", m.IsValid())
	fmt.Println("call:", m.Call(nil)[0].Interface())
}
