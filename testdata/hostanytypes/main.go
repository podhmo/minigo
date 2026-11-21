package main

import (
	"fmt"
	"slices"

	ht "example.com/hostanytypes"
)

type float32 int

func main() {
	s := ht.Make()
	ht.Note(fmt.Sprintf("in-script: %T %T %T %T %T", s[0], s[1], s[2], s[3], s[4]))
	slices.Reverse(s)
	ht.Note(ht.Types())
	ht.Pass(s)
	ht.Note(ht.Types())

	var x float32 = 7
	s2 := []any{x}
	ht.Pass2(s2)
}
