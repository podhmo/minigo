package main

import (
	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualforeign_case/inner"
)

// The case gate shares the operand check with gc's case wording —
// `invalid case i in switch on d (mismatched types int64 and
// time.Duration)` — including the case-first operand order.
func main() {
	var d = inner.Dur
	var i int64
	switch d {
	case i:
		println("hit")
	}
}
