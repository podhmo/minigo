package main

import "github.com/podhmo/minigo/testdata/initembed/inner"

// Assert asserts a struct embedding inner.Conf against an interface it
// does not implement. Resolving the embed runs inner's failing init: the
// failure must surface instead of the method set turning "unsure" and the
// assertion succeeding (yaml.v3's Unmarshaler probe on oapi-codegen's
// configuration once did exactly that).

type U interface{ UnmarshalYAML(n int) error }

type conf struct {
	inner.Conf
	B string
}

func Assert() bool {
	var c conf
	var x any = &c
	_, ok := x.(U)
	return ok
}

func main() {}
