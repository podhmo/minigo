package pkgb

import "github.com/podhmo/minigo/testdata/difffuzz/ifacebase_dynpair/pkga"

// T promotes pkga's m through the embedded interface and declares its
// own m — different unexported methods sharing a name. gc still
// satisfies pkga's interface: the promoted pkga.m is the member that
// requirement resolves to.
type T struct{ pkga.I }

func (T) m() int { return 1 }
