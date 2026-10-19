package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualforeign_nil/inner"
)

// `y == nil` is a gc compile error — Duration is not nilable
// (mismatched types time.Duration and untyped nil). The synthesized
// qualifier still resolves for the nilability check.
func main() {
	var y = inner.Dur
	fmt.Println(y == nil)
}
