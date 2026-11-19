package main

import (
	"slices"
	"time"

	"example.com/lib"
)

// a seq handed to a host any spells func(func(T) bool) when the host
// has T (basic or bound), iter.Seq[...] when the binary carries that
// instantiation; a script element type stays func(func(any) bool).

type Color int

func Spell() string {
	return lib.TypeOf(slices.Values([]time.Duration{1})) + "\n" +
		lib.TypeOf(slices.All([]string{"a"})) + "\n" +
		lib.TypeOf(slices.Values([]Color{1}))
}
