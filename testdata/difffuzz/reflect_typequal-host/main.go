package main

import (
	"bytes"
	"fmt"
	"reflect"
)

// A package-qualified composite type must have ONE identity whether it
// came from a declared typedef (*bytes.Buffer) or the host value it
// holds — the selector's qualifier is a package reference, spelled
// literally, not a local name re-qualified by the enclosing package.

func main() {
	var b bytes.Buffer

	// declared *T type == the type of a *T value
	var p *bytes.Buffer = &b
	var q *bytes.Buffer
	vp := reflect.ValueOf(&p).Elem()
	x := reflect.ValueOf(&b)
	fmt.Println("elem == TypeOf(&b):", vp.Type() == x.Type())
	fmt.Println("nil ptr == TypeOf(&b):", reflect.TypeOf(q) == x.Type())
	fmt.Println("assignable *T -> *T:", x.Type().AssignableTo(vp.Type()))

	// a declared *T slot stores *T like Go (pointer replace)
	vp.Set(x)
	fmt.Println("ptr replaced:", p == &b)

	// slice/map/chan composites over host types unify too
	var sl []bytes.Buffer
	var m map[string]bytes.Buffer
	var c chan bytes.Buffer
	fmt.Println("slice:", reflect.ValueOf(&sl).Elem().Type() == reflect.TypeOf([]bytes.Buffer{}))
	fmt.Println("map:", reflect.ValueOf(&m).Elem().Type() == reflect.TypeOf(map[string]bytes.Buffer{}))
	fmt.Println("chan:", reflect.ValueOf(&c).Elem().Type() == reflect.TypeOf(make(chan bytes.Buffer)))

	// a mismatched assignability still reports the real types
	var i int
	vi := reflect.ValueOf(&i).Elem()
	fmt.Println("assignable *T -> int:", x.Type().AssignableTo(vi.Type()))
}
