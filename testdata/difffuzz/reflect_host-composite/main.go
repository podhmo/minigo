package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

func main() {
	var b bytes.Buffer
	fmt.Println(reflect.TypeOf(b))
	fmt.Println(reflect.TypeOf(&b))
	fmt.Println(reflect.TypeOf(bytes.Buffer{}))
	fmt.Println(reflect.TypeOf(&bytes.Buffer{}))
	fmt.Println(reflect.TypeOf(strings.Reader{}))
	fmt.Println(reflect.TypeOf(strings.Builder{}))
	fmt.Println(reflect.TypeOf(sync.Mutex{}))
	fmt.Println(reflect.TypeOf(&sync.Pool{}))
	p := &sync.Pool{New: func() any { return 7 }}
	fmt.Println(reflect.TypeOf(p), p.Get())
	fmt.Printf("%T %T\n", b, p)
	b.WriteString("xy")
	fmt.Println(b.String())
}
