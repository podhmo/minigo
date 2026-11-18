package main

type I interface{ M() string }
type U struct{}

func (U) M() string { return "u" }

// U.M (declared, depth 1) vs I.M (depth 1): gc says ambiguous selector.
type T1 struct {
	U
	I
}

// I.M at depth 1 must beat the depth-2 contest between W1.A and W2.B.
type A struct{}

func (A) M() string { return "a" }

type B struct{}

func (B) M() string { return "b" }

type W1 struct{ A }
type W2 struct{ B }
type T2 struct {
	I
	W1
	W2
}

// defined type over a struct promotes through the base's own embeds.
type Inner struct{}

func (Inner) M() string { return "inner" }

type Base struct{ Inner }
type Wrap Base
type T3 struct{ Wrap }
type T4 struct {
	Wrap
	W2
}

func F1() string {
	var t T1
	t.U = U{}
	return t.M() // gc: ambiguous selector t.M
}

func F2() string {
	var t T2
	t.I = U{}
	return t.M() // gc: I.M wins at depth 1 → "u"
}

func F3() string {
	var t T3
	return t.M() // gc: "inner" via Wrap→Inner
}

func F4() string {
	var t T4
	return t.M() // gc: ambiguous selector t.M
}
