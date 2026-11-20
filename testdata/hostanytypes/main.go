package main

import (
	"fmt"
	"slices"

	ht "example.com/hostanytypes"
)

func main() {
	s := ht.Make()
	ht.Note(fmt.Sprintf("in-script: %T %T %T %T", s[0], s[1], s[2], s[3]))
	slices.Reverse(s)
	ht.Note(ht.Types())
	ht.Pass(s)
	ht.Note(ht.Types())
}
