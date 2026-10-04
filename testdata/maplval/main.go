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

// Field writes off a copied struct element trap even when the field
// itself is reference-shaped — m[k].f = v writes the copy, never the
// stored element. Writing *into* the field's own backing store is the
// legal shape pinned below.
func MapStructSliceFieldAssign() {
	m := map[string]g{"k": {f: []int{1}}}
	m["k"].f = []int{9}
}

// An array field of a copied struct element stays a copy: m[k].a[i]
// reaches the copy's backing array, not the stored one.
func MapStructArrFieldIndex() {
	m := map[string]g{"k": {a3: [3]int{1, 2, 3}}}
	m["k"].a3[0] = 9
}

// A struct field of a copied struct element is still a copy two hops
// out: m[k].g2.x writes the copy's inner struct.
func MapStructInnerField() {
	m := map[string]g{"k": {gi: struct{ x int }{x: 1}}}
	m["k"].gi.x = 9
}

// A map field is shared, so m[k].ms2[k2] = v is legal — but the
// element of that map is again a copy, and writing its field must trap.
func MapStructMapElemField() {
	m := map[string]g{"k": {ms2: map[string]s{"q": {x: 1}}}}
	m["k"].ms2["q"].x = 9
}

// Legal write-through shapes: every hop lands in storage shared with
// the map element (a stored pointer, or a reference-shaped field).
// Each function performs one such write and returns the mutated cell.

// Pointer elements deref to shared storage; pa["a"][i] = v writes the
// pointee's backing array.
func PtrArrElemIndex() int {
	m := map[string]*[3]int{"a": {1, 2, 3}}
	m["a"][1] = 9
	return m["a"][1]
}

// A slice field keeps its backing array across the element copy:
// m[k].f[i] = v writes the shared slice.
func StructSliceFieldIndex() int {
	m := map[string]g{"k": {f: []int{1, 2, 3}}}
	m["k"].f[1] = 9
	return m["k"].f[1]
}

// A map field is shared: m[k].ms[k2] = v inserts into the stored map.
func StructMapFieldIndex() int {
	m := map[string]g{"k": {ms: map[string]int{}}}
	m["k"].ms["q"] = 7
	return m["k"].ms["q"]
}

// A pointer field is shared: m[k].p.x = v writes through the stored
// pointer.
func StructPtrFieldX() int {
	m := map[string]g{"k": {p: &s{x: 1}}}
	m["k"].p.x = 42
	return m["k"].p.x
}

// *m[k].p = v writes the pointee through the stored pointer.
func StructPtrFieldDeref() int {
	m := map[string]g{"k": {p: &s{x: 1}}}
	*m["k"].p = s{x: 5}
	return m["k"].p.x
}

// m[k].a[i] = v is legal when the array's element is itself
// reference-shaped: the inner slice's backing is shared.
func StructArrSliceField() int {
	m := map[string]g{"k": {a2: [2][]int{{1}, {2}}}}
	m["k"].a2[1][0] = 22
	return m["k"].a2[1][0]
}

// m[k].ss[i].x = v is legal: the slice field is shared and its struct
// elements are addressable storage.
func StructSliceOfStruct() int {
	m := map[string]g{"k": {ss: []s{{x: 1}}}}
	m["k"].ss[0].x = 33
	return m["k"].ss[0].x
}

// m[k].g2.s2[i] = v is legal: the inner struct field is a copy, but
// its slice field still shares its backing array.
func StructInnerSlice() int {
	m := map[string]g{"k": {g2: struct{ s2 []int }{s2: []int{1}}}}
	m["k"].g2.s2[0] = 8
	return m["k"].g2.s2[0]
}

type g struct {
	f   []int
	ms  map[string]int
	ms2 map[string]s
	p   *s
	a2  [2][]int
	a3  [3]int
	ss  []s
	gi  struct{ x int }
	g2  struct{ s2 []int }
}
