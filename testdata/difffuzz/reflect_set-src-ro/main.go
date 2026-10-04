package main

import (
	"bytes"
	"fmt"
	"reflect"
)

type H struct {
	hidden int
	Y      int
}

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
	h := H{hidden: 1, Y: 2}
	src := reflect.ValueOf(&h).Elem().Field(0) // value read through unexported field
	fmt.Println("src CanInterface/CanSet:", src.CanInterface(), src.CanSet())

	// a settable dst with a ro source of the same type
	try("ro-src-sametype", func() {
		dst := reflect.ValueOf(&h.hidden).Elem()
		dst.Set(src)
	})
	// the ro gate fires before assignability, not the type-mismatch panic
	try("ro-src-wrongtype", func() {
		hs := struct{ hidden string }{hidden: "s"}
		srcStr := reflect.ValueOf(&hs).Elem().Field(0)
		dst := reflect.ValueOf(&h.Y).Elem()
		dst.Set(srcStr)
	})
	// unsettable dst + ro src: the dst gate still wins
	try("ro-src-unsettable-dst", func() {
		reflect.ValueOf(h).Field(1).Set(src)
	})
	// an ro source carried through an interface keeps its flag
	try("ro-src-iface", func() {
		var i any = h
		isrc := reflect.ValueOf(i).Field(0)
		dst := reflect.New(reflect.TypeOf(0)).Elem()
		dst.Set(isrc)
	})
	// ro source into a host-domain dst
	try("ro-src-hostdst", func() {
		var i any = h
		isrc := reflect.ValueOf(i).Field(0)
		dst := reflect.New(reflect.TypeOf(0)).Elem()
		dst.Set(isrc)
	})
	// zero source into a host-domain dst
	try("zero-src-hostdst", func() {
		var b bytes.Buffer
		var zero reflect.Value
		dst := reflect.New(reflect.TypeOf(b)).Elem()
		dst.Set(zero)
	})
	// ro dst + ro src: the dst gate wins
	try("rodst-ro-src", func() {
		rodst := reflect.ValueOf(&h).Elem().Field(0)
		rodst.Set(reflect.ValueOf(&h).Elem().Field(0))
	})
	// reading the ro source is still fine
	fmt.Println("src read:", src.Int())
}
