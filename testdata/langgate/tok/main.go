//go:build go1.18

package main

// the file's own //go:build go1.18 constraint replaces the module's
// go1.17 lang — generics are legal here.

func F[T any](x T) T { return x }

func Answer() int { return F(42) }

func main() {}
