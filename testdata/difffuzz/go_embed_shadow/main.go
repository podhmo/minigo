package main

import (
	_ "embed"
	"fmt"
)

// a user variable named like the hidden embed builtin must not shadow
// it — the builtin's name is unwritable in Go source.
var __minigo_embed__ = 42

//go:embed hello.txt
var s string

func main() {
	fmt.Println(s, __minigo_embed__)
}
