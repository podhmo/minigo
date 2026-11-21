package main

// Slot-classification corpus (P4-2a): every function exists so a test
// can assert whether its chunk carries a boxed slot. Nothing runs.
import (
	d "example.com/define"
)

type Counter struct{ N int }

func (c Counter) Val() int { return c.N }
func (c *Counter) Bump()   { c.N++ }

type U struct{ K int }

func (u *U) PP() {}

type S struct {
	F int
	M func()
	U // promotes PP (pointer receiver)
}

type E struct{ V int }

func (e *E) P()    {}
func (e E) Q() int { return e.V }

type I interface{ Do() }

func NewS() S { return S{} }

// plain store: the ref dies with the store — no box.
func Store() int { x := 0; x = 3; return x }

// &x: the ref IS the result — box.
func Addr() *int { x := 0; return &x }

// &x.f keeps a ref base on x — box.
func AddrField() *int { s := S{}; return &s.F }

// &xs[i] chains a VALUE of the container — no box.
func AddrElem() *int { xs := []int{1}; return &xs[0] }

// a field read consumes the ref — no box.
func FieldRead() int { s := S{F: 1}; return s.F }

// value-receiver call — no box.
func ValueMethodCall() int { c := Counter{}; return c.Val() }

// pointer-receiver call — box.
func PtrMethodCall() { c := Counter{}; c.Bump() }

// a func field call is a field select — no box.
func FuncFieldCall() { s := S{}; s.M() }

// method values: pointer receiver boxes, value receiver does not.
func ValueMethodValue() func() int { c := Counter{}; return c.Val }
func PtrMethodValue() func()       { c := Counter{}; return c.Bump }

// interface dispatch is dynamic — no box.
func IfaceCall(w I) { w.Do() }

// a promoted member could be a pointer method — box.
func PromotedCall() { s := S{}; s.PP() }

// a map element is a detached copy — no box.
func MapElem() { m := map[string]E{}; m["k"].P() }

// a slice element's method binds the container's cell — box.
func SliceElemMethod() { xs := []E{}; xs[0].P() }

// a captured local must stay boxed for the closure.
func Closure() func() { x := 0; return func() { x++ } }

// the inferred operand type resolves: a promoted member could be a
// pointer method — box; a direct field is not.
func InferPromoted()  { s := NewS(); s.PP() }
func InferField() int { s := NewS(); return s.F }

// a callee whose result type does not resolve leaves the operand
// unresolvable — box.
func Unknown() { fs := map[int]func() S{0: NewS}; s := fs[0](); s.PP() }

// a *T operand is already a pointer — no box.
func PtrTyped(c *Counter) { c.Bump() }

// deferred pointer-receiver call — box.
func DeferPtr(c Counter) { defer c.Bump() }

// a special form sees its caller's locals — all boxed.
func Special() {
	x := 0
	d.Convert(func(c *d.Config) {
		_ = x
	})
}

// an alias's member set is the target's — Bump is a pointer method.
type CA = Counter

func AliasMethod() { c := CA{}; c.Bump() }

// the parameter's S is the package type — the local `type S int`
// shadow cannot hide its promoted pointer method.
func ShadowedParam(s S) {
	type S int
	_ = S(0)
	s.PP()
}
