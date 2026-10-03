// run

// Pin for the append.go corpus fixes: string spread, element-type
// coercion of materialized constants, and script-side DeepEqual.

package main

import (
	"fmt"
	"reflect"
)

func main() {
	// append([]byte, s...) spreads the string's bytes.
	b := append([]byte{}, "hi"...)
	if len(b) != 2 || b[0] != 'h' || b[1] != 'i' {
		panic("spread")
	}
	b = append(b, "!"...)
	if string(b) != "hi!" {
		panic("spread2")
	}

	// a materialized constant converts to the declared element type.
	if !reflect.DeepEqual(append([]float64{}, 0), []float64{0}) {
		panic("float64")
	}
	if !reflect.DeepEqual(append([]complex128{}, 0), []complex128{0}) {
		panic("complex128")
	}

	// DeepEqual: recursive slices, maps, anonymous structs, nil-ness.
	if !reflect.DeepEqual([]int{1, 2}, []int{1, 2}) {
		panic("slice")
	}
	if reflect.DeepEqual([]int{1, 2}, []int{1, 3}) {
		panic("slice2")
	}
	if !reflect.DeepEqual(map[string]int{"a": 1}, map[string]int{"a": 1}) {
		panic("map")
	}
	if !reflect.DeepEqual(make([]struct{}, 2), make([]struct{}, 2)) {
		panic("anon struct")
	}
	var ns []int
	if reflect.DeepEqual(ns, []int{}) {
		panic("nil vs empty")
	}
	fmt.Println("ok")
}
