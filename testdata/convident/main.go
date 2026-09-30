package main

import (
	foa "github.com/podhmo/minigo/testdata/convfooa"
	fob "github.com/podhmo/minigo/testdata/convfoob"
)

// CrossPkgSliceCast: foa.S and fob.S both spell "[]Foo" textually but
// the elements are different declarations — Go rejects the conversion.
func CrossPkgSliceCast() int {
	a := foa.S{foa.Foo{X: 1}}
	b := fob.S(a)
	return len(b)
}

// SamePkgSliceCast: L's element type is literally convfoob.Foo — the
// conversion from fob.S stays legal.
type L []fob.Foo

func SamePkgSliceCast() int {
	b := fob.S{fob.Foo{X: 1}}
	l := L(b)
	return len(l) + l[0].X
}

// AliasElemCast: an explicit alias to the same package resolves equally.
type L2 = []fob.Foo

func AliasElemCast() int {
	b := fob.S{fob.Foo{X: 2}}
	l := L2(b) // alias to the anonymous type: same element decl
	return len(l) + l[0].X
}

func main() {}
