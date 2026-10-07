package main

import (
	. "github.com/podhmo/minigo/testdata/dotextra"
	. "github.com/podhmo/minigo/testdata/greet"
	. "github.com/podhmo/minigo/testdata/lazyboom"
)

func Greeting() string { return Hello("x") }

func Number() int { return Value() + Count }

func TouchBoom() int { return Get() }

func readCount() int { return Count }

// CountDelta reads a dot-imported var through one call site before and
// after the owning package changes it: dot-imported names live in
// another package's env, so the site must not cache the value.
func CountDelta() int {
	a := readCount()
	Inc()
	return readCount() - a
}
