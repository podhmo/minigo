package main

// Pins for the map-element lvalue gate: Go rejects interior writes
// (`m[k][i] = v`, `m[k].f = v`) when the element is copied on read —
// arrays, structs, scalars, and named array types. Only reference-shaped
// elements (slice, map, pointer, chan, func) may be written through.
// Each function performs exactly one such write; callers assert the trap.

type s struct{ x int }
type arr [3]int

func MapArrIndex() {
	m := map[string][3]int{"a": {1, 2, 3}}
	m["a"][1] = 9
}

func MapStructField() {
	m := map[string]s{"a": {x: 1}}
	m["a"].x = 9
}

func MapNamedArrIndex() {
	m := map[string]arr{"a": {1, 2, 3}}
	m["a"][1] = 9
}

func MapScalarIndex() {
	m := map[string]string{"a": "abc"}
	m["a"][0] = 'x'
}
