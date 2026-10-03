package main

import (
	"fmt"
	"reflect"
)

func main() {
	// distinct pointer keys with equal pointees — different keys in Go
	fmt.Println(reflect.DeepEqual(
		map[*int]int{new(int): 1},
		map[*int]int{new(int): 1},
	))
	// same value keys — equal
	fmt.Println(reflect.DeepEqual(map[string]int{"a": 1}, map[string]int{"a": 1}))
	// same pointer — same key
	k := new(int)
	fmt.Println(reflect.DeepEqual(map[*int]int{k: 1}, map[*int]int{k: 1}))
	// struct key — equal by value
	type P struct{ X, Y int }
	fmt.Println(reflect.DeepEqual(map[P]int{P{1, 2}: 3}, map[P]int{P{1, 2}: 3}))
	// different values — not equal
	fmt.Println(reflect.DeepEqual(map[string]int{"a": 1}, map[string]int{"a": 2}))
}
