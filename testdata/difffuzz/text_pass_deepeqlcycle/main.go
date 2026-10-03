package main

import (
	"fmt"
	"reflect"
)

// DeepEqual on cyclic structures must terminate — Go treats a
// revisited pair coinductively as equal rather than recursing forever.
func main() {
	m := map[string]any{}
	m["self"] = m
	fmt.Println(reflect.DeepEqual(m, m))

	s := []any{}
	s = append(s, s)
	fmt.Println(reflect.DeepEqual(s, s))

	type P struct{ Next *P }
	p1, p2 := &P{}, &P{}
	p1.Next, p2.Next = p2, p1
	fmt.Println(reflect.DeepEqual(p1, p2))

	// unequal cycle shapes still report false
	p2.Next = &P{}
	fmt.Println(reflect.DeepEqual(p1, p2))
}
