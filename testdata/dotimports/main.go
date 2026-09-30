package main

import (
	. "github.com/podhmo/minigo/testdata/dotextra"
	. "github.com/podhmo/minigo/testdata/greet"
	. "github.com/podhmo/minigo/testdata/lazyboom"
)

func Greeting() string { return Hello("x") }

func Number() int { return Value() + Count }

func TouchBoom() int { return Get() }
