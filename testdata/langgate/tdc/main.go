//go:build go1.26

package main

// the tag lifts this file to go1.26 — still short of generic methods'
// go1.27, and the error reports the file's own //go:build version.

type List[E any] []E

func (l List[E]) Reduce[R any](init R, f func(R, E) R) R { return init }

func main() {}
