package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
)

func main() {
	t := reflect.TypeOf((*strings.Reader)(nil))
	fmt.Println(t)
	fmt.Println(t.Elem())
	fmt.Println(t.Elem().Kind(), t.Elem().PkgPath())
	b := reflect.TypeOf((*bytes.Buffer)(nil))
	fmt.Println(b, b.Elem())
	fmt.Println(strings.NewReader("ab").Len())
	var r strings.Reader
	fmt.Println(r.Len())
}
