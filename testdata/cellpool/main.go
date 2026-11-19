package main

// Regression probes for pooled promotion cells: a select ref on a
// member whose receiver binding is compile-unprovable (generic
// receiver -> cellUnknown) can publish the promoted cell, so the ref
// is emitted with B=1 and promotes to the heap — a pooled cell would
// be cleared when its frame returns.

type Box[T any] struct{ v T }

func (b *Box[T]) Set(v T) { b.v = v }
func (b *Box[T]) Get() T  { return b.v }

// Pair binds two pointer method values to a local generic value; the
// BoundMethod receivers hold the promoted cell past Pair's return.
func Pair() (func(int), func() int) {
	b := Box[int]{v: 0}
	return b.Set, b.Get
}

func Main() int {
	set, get := Pair()
	set(7)
	return get()
}

type Arr[T any] struct{ A [2]int }

// Alias slices a local generic value's array field: the slice header
// borrows the field's storage through a ref that the slice result
// publishes.
func Alias() []int {
	a := Arr[int]{}
	return a.A[:]
}

func Slice() int {
	s := Alias()
	s[0] = 3
	return s[0]
}
