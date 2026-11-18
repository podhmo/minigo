package main

// COMPILE_TIME_FACT: a negated opaque call never guesses builtin bool

import (
	"fmt"

	inner "github.com/podhmo/minigo/testdata/difffuzz/gate_opaquebool/inner"
)

func main() {
	var b inner.B = true
	// !inner.Flag() has type inner.B — under strict lazy the call's
	// result is a deferred fact, so the gate must stay out rather
	// than compare a guessed builtin bool against B
	fmt.Println(!inner.Flag() == b)
}
