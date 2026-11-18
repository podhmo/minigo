package main

import (
	"fmt"
	"net"
)

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

// an interface requirement promoted through two embeds keeps its
// signature shell: W00's M is M() int — it does not implement M()
// string, and it does implement I0.
type I0 interface{ M() int }
type J interface{ I0 }
type W0 struct{ J }
type W00 struct{ W0 }
type ISig interface{ M() string }

// a promoted host FIELD wins the selector but is no method.
type W struct{}

func (W) Port() string { return "m" }

type WW struct{ W }
type T6 struct {
	net.TCPAddr
	WW
}
type PortI interface{ Port() string }

// defined pointer types promote nothing their pointee carries.
type PA = *Base
type P PA
type Q *Base

// a recursive generic embed is a legal declaration — the method set is
// a fixpoint.
type Self[A any] struct {
	*Self[A]
}

func (Self[A]) Tag() {}

type TI interface{ Tag() }

func F5() (r string) {
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	var a any = W00{}
	_ = a.(ISig) // gc: missing method M (M is M() int)
	return
}

func F5ok() string {
	var a any = W00{}
	if _, ok := a.(I0); ok {
		return "ok"
	}
	return "ng"
}

func F6() (r string) {
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	var a any = T6{}
	_ = a.(PortI) // gc: T6.Port is a field, not a method
	return
}

func F6sel() string {
	var t T6
	return fmt.Sprint(t.Port) // the field wins: "0"
}

func F8() (r string) {
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	var a any = P(nil)
	_ = a.(I) // gc: P has no method M
	return
}

func F8b() (r string) {
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	var a any = Q(nil)
	_ = a.(I) // gc: Q has no method M either
	return
}

func F9() string {
	var a any = Self[int]{}
	if _, ok := a.(TI); ok {
		return "ok"
	}
	return "ng"
}

// parens around the underlying expression must not stall the walk.
type NInt (int)
type NPtr *(Base)
type NPtr2 (*(Base))

// a winning host FIELD stays a member: one level out it contests a
// same-depth method — gc's answer is ambiguous, not method-wins.
type WQ1 struct{}

func (WQ1) Port() string { return "wq" }

type WQ struct{ WQ1 }
type T7 struct {
	T6
	WQ
}

// distinct defined pointer types are distinct nil values.
type PS *Base
type PT *Inner

func F10() string {
	var n NInt
	var p NPtr
	var p2 NPtr2
	return fmt.Sprint(n, p, p2) // "0 <nil> <nil>"
}

func F7() (r string) {
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	var a any = T7{}
	_ = a.(PortI) // gc: T7.Port is ambiguous — does not implement
	return
}

func F11() string {
	var a any = PS(nil)
	var b any = PT(nil)
	return fmt.Sprint(a == b) // "false" — different dynamic types
}

// generic defined pointers: nil converts, and the rebuilt underlying
// keeps the binds so GP[int]'s *T compares as *int.
type GP[T any] *T
type GQ[T any] *T

func F12() string {
	p := GP[int](nil)
	var q GP[int] = nil
	return fmt.Sprint(p == nil, q == nil, any(p) == any(GP[int](nil)), any(p) == any(GQ[int](nil)))
	// "true true true false"
}

// an early (embed-driven) instantiation interned before the runtime
// mint: the pointer receiver's own parameter name `U` must still bind
// to the concrete argument — the interned descriptor carries it.
type PT2[T any] struct{ v T }

func (p *PT2[U]) Get() U { return p.v }

type PE2 struct{ PT2[int] }

func F13() string {
	b := &PE2{}
	b.v = 7
	return fmt.Sprint(b.Get()) // "7"
}
