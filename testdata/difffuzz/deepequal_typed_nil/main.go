package main

// reflect.DeepEqual's arguments are interfaces: a typed nil, bare or
// already boxed in an `any`, carries a dynamic type, so it is not
// DeepEqual to the nil interface and equals only a nil of the same type
// — a nil host *os.File (os.Open on error) included, against the
// script's (*os.File)(nil).

import (
	"fmt"
	"os"
	"reflect"
)

type T struct{ n int }

type holder struct {
	F *os.File
	A any
}

func main() {
	f, _ := os.Open("/nonexistent")
	var a any = f
	fmt.Println(reflect.DeepEqual(a, nil), reflect.DeepEqual(nil, a))
	fmt.Println(reflect.DeepEqual(f, nil))
	var s *T
	var b any = s
	fmt.Println(reflect.DeepEqual(b, nil), reflect.DeepEqual(s, nil))
	fmt.Println(reflect.DeepEqual(f, (*os.File)(nil)))
	var g *os.File
	fmt.Println(reflect.DeepEqual(f, g), reflect.DeepEqual(a, g), reflect.DeepEqual(a, any(g)))
	fmt.Println(reflect.DeepEqual(b, s), reflect.DeepEqual(b, (*T)(nil)), reflect.DeepEqual(b, f))
	var e error
	var x any
	fmt.Println(reflect.DeepEqual(e, nil), reflect.DeepEqual(x, nil), reflect.DeepEqual(e, x))
	fmt.Println(reflect.DeepEqual([]*os.File{f}, []*os.File{nil}), reflect.DeepEqual([]any{f}, []any{nil}))
	fmt.Println(reflect.DeepEqual([]any{a}, []any{g}))
	fmt.Println(reflect.DeepEqual(map[string]any{"a": f}, map[string]any{"a": nil}))
	var sl []int
	fmt.Println(reflect.DeepEqual(sl, nil), reflect.DeepEqual(sl, []int(nil)), reflect.DeepEqual(sl, []int{}))
	fmt.Println(reflect.DeepEqual(holder{F: f}, holder{}), reflect.DeepEqual(holder{A: f}, holder{}),
		reflect.DeepEqual(holder{A: f}, holder{A: (*os.File)(nil)}))
}
