//go:build go1.18

package main

// the generic decl is legal in this file's own go1.18 lang; calls from
// the untagged file are implicit instantiation at go1.17.
func Id[T any](x T) T { return x }
