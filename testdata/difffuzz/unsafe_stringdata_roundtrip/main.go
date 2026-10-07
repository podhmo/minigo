package main

import (
	"fmt"
	"unsafe"
)

type stringptr unsafe.Pointer

type Label struct {
	packed  uint64
	untyped any
}

func OfString(v string) Label {
	return Label{packed: uint64(len(v)), untyped: stringptr(unsafe.StringData(v))}
}

func (t Label) UnpackString() string {
	if v, ok := t.untyped.(stringptr); ok {
		return unsafe.String((*byte)(v), int(t.packed))
	}
	return ""
}

func main() {
	fmt.Printf("%q\n", OfString("").UnpackString())
	l := OfString("hello")
	fmt.Println(l.UnpackString(), l.packed)
	b := []byte("abc")
	fmt.Println(unsafe.String(unsafe.SliceData(b), len(b)), string(unsafe.Slice(unsafe.StringData("xyz"), 3)))
	s := unsafe.Slice(unsafe.SliceData(b), 2)
	s[0] = 'X'
	fmt.Println(string(b), len(s), unsafe.String(unsafe.StringData(""), 0) == "")
}
