package main

import "probehost"

// S embeds a nil *T — Go resolves the selector statically, so what
// happens next depends on the member kind.

type S struct{ *probehost.T }

// NilMethod: a pointer-receiver method takes the nil receiver — Go
// passes it straight through and M returns 7.
func NilMethod() int {
	s := S{T: probehost.Nil}
	return s.M()
}

// NilValueMethod: a value-receiver method must dereference the nil
// receiver — panics like Go.
func NilValueMethod() int {
	s := S{T: probehost.Nil}
	return s.V()
}

// NilField: a field read must dereference the nil pointer — panics
// like Go.
func NilField() int {
	s := S{T: probehost.Nil}
	return s.N
}
