// run

// Local const blocks: iota counts the spec index and a declared
// `const iota` shadows the builtin like any local name.

package main

const X = 2

func main() {
	const (
		a = iota
		b
		c = iota + 10
		d
	)
	if a != 0 || b != 1 || c != 12 || d != 13 {
		panic("basic iota")
	}

	const (
		A    = iota // 0
		iota = iota // 1
		B           // 1 (the declared const iota, not the counter)
		C           // 1
	)
	if A != 0 || B != 1 || C != 1 {
		panic("shadowed iota")
	}

	const (
		X = X + X
		Y
		Z = iota // 1 — still the declared const iota
	)
	if X != 4 || Y != 8 || Z != 1 {
		panic("cross-block iota")
	}
}
