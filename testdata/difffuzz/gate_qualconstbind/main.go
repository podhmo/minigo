package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualconstbind/inner"
)

// `var y = inner.C` binds y as rune — gc rejects `y == i` (mismatched
// types rune and int). The operand gate reads the qualified const's
// default through the source package's index; its Globals stay empty
// until init runs.
func main() {
	var y = inner.C
	var i int
	fmt.Println(y == i)
}
