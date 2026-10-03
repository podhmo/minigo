package main

import (
	"fmt"
	"reflect"
)

var s0 = "hi"
var i0 = 42
var slice = []int{1, 2}
var bts = []byte("ab")
var m = map[string]int{"a": 1}
var nilmap map[string]int
var nilslice []int
var hidden = struct {
	x int
	Y int
}{x: 1, Y: 2}

func try(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v\n", name, r)
		}
	}()
	f()
}

func main() {
	// scalar setters: the settable gate runs before the kind gate, and
	// the panic names the setter — Go's mustBeAssignable order.
	try("SetInt-unaddr", func() { reflect.ValueOf(i0).SetInt(7) })
	try("SetInt-wrongkind-unaddr", func() { reflect.ValueOf(s0).SetInt(7) })
	try("SetInt-wrongkind-addr", func() { reflect.ValueOf(&s0).Elem().SetInt(7) })
	try("SetInt-ro", func() { reflect.ValueOf(&hidden).Elem().Field(0).SetInt(7) })
	try("SetString-unaddr", func() { reflect.ValueOf(i0).SetString("zz") })
	try("SetBool-unaddr", func() { reflect.ValueOf(i0).SetBool(true) })
	try("SetUint-unaddr", func() { reflect.ValueOf(i0).SetUint(9) })
	try("SetFloat-unaddr", func() { reflect.ValueOf(i0).SetFloat(2.5) })
	try("SetComplex-unaddr", func() { reflect.ValueOf(i0).SetComplex(1 + 2i) })
	try("SetComplex-addr-struct", func() { reflect.ValueOf(&hidden).Elem().Field(1).SetComplex(1i) })
	// Set: settable, then source validity, then assignability.
	try("Set-unaddr-assign", func() { reflect.ValueOf(i0).Set(reflect.ValueOf(s0)) })
	try("Set-addr-assign", func() { reflect.ValueOf(&s0).Elem().Set(reflect.ValueOf(i0)) })
	try("Set-invalid-x", func() { reflect.ValueOf(&s0).Elem().Set(reflect.ValueOf(nilmap).MapIndex(reflect.ValueOf("zzz"))) })
	// SetBytes: `reflect.Value.SetBytes of non-byte slice` has no prefix.
	try("SetBytes-nonslice", func() { reflect.ValueOf(&s0).Elem().SetBytes([]byte("zz")) })
	try("SetBytes-nonbyte", func() { reflect.ValueOf(&slice).Elem().SetBytes([]byte("zz")) })
	try("SetBytes-unaddr", func() { reflect.ValueOf(bts).SetBytes([]byte("zz")) })
	// SetLen/SetCap reslice checks.
	try("SetLen-nonslice", func() { reflect.ValueOf(&i0).Elem().SetLen(1) })
	try("SetLen-unaddr", func() { reflect.ValueOf(slice).SetLen(1) })
	try("SetLen-overcap", func() { s := make([]int, 1, 2); reflect.ValueOf(&s).Elem().SetLen(5) })
	try("SetLen-ro", func() { reflect.ValueOf(&hidden).Elem().Field(0).SetLen(1) })
	try("SetCap-nonslice", func() { reflect.ValueOf(&i0).Elem().SetCap(1) })
	try("SetCap-unaddr", func() { reflect.ValueOf(slice).SetCap(1) })
	// iterator setters: before-Next first, then MapIter.SetKey/SetValue.
	it := reflect.ValueOf(m).MapRange()
	try("SetIterKey-beforeNext", func() { reflect.ValueOf(&s0).Elem().SetIterKey(it) })
	try("SetIterValue-beforeNext", func() { reflect.ValueOf(&i0).Elem().SetIterValue(it) })
	try("SetIterKey-unaddr", func() { reflect.ValueOf(i0).SetIterKey(it) })
	it2 := reflect.ValueOf(m).MapRange()
	it2.Next()
	try("SetIterKey-wrongkind", func() { reflect.ValueOf(&i0).Elem().SetIterKey(it2) })
	// nil map: insert panics, delete is a no-op; assignability still
	// rules before the nil backing is touched.
	try("SetMapIndex-nilmap", func() { reflect.ValueOf(nilmap).SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf(1)) })
	try("SetMapIndex-nilmap-del", func() {
		reflect.ValueOf(nilmap).SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf(m).MapIndex(reflect.ValueOf("zzz")))
	})
	try("SetMapIndex-badkey", func() { reflect.ValueOf(nilmap).SetMapIndex(reflect.ValueOf(1), reflect.ValueOf(2)) })
	try("SetMapIndex-badval", func() { reflect.ValueOf(m).SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf("x")) })
	// call gates.
	try("Call-int", func() { reflect.ValueOf(i0).Call(nil) })
	try("CallSlice-int", func() { reflect.ValueOf(i0).CallSlice(nil) })
	// Overflow* gates and the zero-Value panic.
	try("OverflowInt-str", func() { reflect.ValueOf(s0).OverflowInt(1) })
	try("OverflowInt-int", func() { fmt.Println("OverflowInt-int:", reflect.ValueOf(i0).OverflowInt(1)) })
	try("OverflowUint-str", func() { reflect.ValueOf(s0).OverflowUint(1) })
	try("OverflowFloat-str", func() { reflect.ValueOf(s0).OverflowFloat(1) })
	try("OverflowFloat-f64", func() { fmt.Println("OverflowFloat-f64:", reflect.ValueOf(1.5).OverflowFloat(1e50)) })
	try("OverflowComplex-str", func() { reflect.ValueOf(s0).OverflowComplex(complex(1, 2)) })
	try("CanInterface-zero", func() { reflect.ValueOf(nilmap).MapIndex(reflect.ValueOf("zz")).CanInterface() })
	try("Interface-zero", func() { reflect.ValueOf(nilmap).MapIndex(reflect.ValueOf("zz")).Interface() })
	// nil slice index/slice bounds.
	try("Index-nilslice", func() { reflect.ValueOf(nilslice).Index(9) })
	try("Slice-nilslice", func() { reflect.ValueOf(nilslice).Slice(1, 3) })
	// FieldByIndex uses `Field` wording past the first level.
	try("FieldByIndex-deep", func() { reflect.TypeOf(struct{ A int }{1}).FieldByIndex([]int{0, 0}) })
	try("FieldByIndex-top", func() { reflect.TypeOf(&slice).FieldByIndex([]int{0}) })
	// `byte` spells `uint8` in reflect panic types.
	try("Key-nonmap-byte", func() { reflect.ValueOf(bts).Index(0).Type().Key() })
	// kind-gated reads keep the single `reflect:` prefix.
	try("IsNil-struct", func() { reflect.ValueOf(hidden).IsNil() })
	try("Float-int", func() { reflect.ValueOf(i0).Float() })
	try("Int-str", func() { reflect.ValueOf(s0).Int() })
}
