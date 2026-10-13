package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualshadow/shadowinner"
)

// `const false` shadows the universe literal inside THIS package — a
// var bound from it is rune-typed, so `w == r2` compares runes like gc.
const false = 'b'

func main() {
	// shadowinner.C's value is the SHADOWED `true` — rune 'a', not
	// universe bool — so `y == r` compares runes and prints true.
	var y = shadowinner.C
	var r rune = 'a'
	fmt.Println(y == r)

	var w = false
	var r2 rune = 'b'
	fmt.Println(w == r2)

	// a function-local decl shadows the import name — `shadowinner.D`
	// reads the field, never the package's const member.
	var shadowinner = struct{ D string }{D: "x"}
	fmt.Println(shadowinner.D == "x")
}
