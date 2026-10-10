package main

import (
	"errors"
	"fmt"
)

// E is the concrete error the As/AsType targets bind to.
type E struct{ v int }

func (e E) Error() string { return fmt.Sprintf("e%d", e.v) }

// Extra is an interface target: error plus one more method.
type Extra interface {
	error
	Extra()
}

// XE implements Extra — the positive control for the interface target.
type XE struct{}

func (XE) Error() string { return "xe" }
func (XE) Extra()        {}

// W is a plain single-cause wrapper.
type W struct{ inner error }

func (w W) Error() string { return "w: " + w.inner.Error() }
func (w W) Unwrap() error { return w.inner }

// A answers through its own As(any) bool — the custom-As hook errors.As
// calls on each node before unwrapping.
type A struct{}

func (A) Error() string { return "a" }

func (A) As(target any) bool {
	if p, ok := target.(*E); ok {
		*p = E{v: 7}
		return true
	}
	return false
}

// J fans out through a declared `Unwrap() []error` — the multi-child
// shape errors.Join uses.
type J struct{ kids []error }

func (j J) Error() string   { return "join" }
func (j J) Unwrap() []error { return j.kids }

func main() {
	// an interface target needs the element's whole method set:
	// E offers only Error(), so it misses Extra.
	_, ok := errors.AsType[Extra](E{})
	fmt.Println(ok) // false
	xe, ok := errors.AsType[Extra](XE{})
	fmt.Println(ok, xe)

	// errors.Join children hide behind Unwrap() []error — the first
	// matching one wins in depth-first order.
	got, ok := errors.AsType[E](errors.Join(W{inner: XE{}}, E{v: 9}))
	fmt.Println(ok, got)

	// the same multi-child walk on a declared `Unwrap() []error`.
	got, ok = errors.AsType[E](J{kids: []error{W{inner: XE{}}, E{v: 3}}})
	fmt.Println(ok, got)

	// the error's own As(any) bool runs before unwrapping — errors.As
	// writes through the user's cell, AsType returns the stored value.
	var e E
	fmt.Println(errors.As(A{}, &e), e)
	got, ok = errors.AsType[E](A{})
	fmt.Println(ok, got)

	// a typed nil boxed in an error interface is still a matchable
	// error value, not a nil error.
	var p *E
	var err error = p
	gotp, ok := errors.AsType[*E](err)
	fmt.Println(ok, gotp == nil)
}
