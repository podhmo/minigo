package main

import (
	"slices"

	"example.com/lib"
)

// seq of a host-bound struct type: the elements marshal to the host
// as *T (the allocator's pointer) while the func(func(T) bool) shape
// spells T — the values must not arrive as zeros.

func Total() string {
	return lib.Total(slices.Values([]lib.P{{X: 1}, {X: 2}}))
}
