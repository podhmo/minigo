package stdfmt

import (
	"fmt"
	"testing"
)

func TestDbg(t *testing.T) {
	spec, ops := rewriteWrapVerbsOps("%[2]*w %d", []any{"x"}, []string{"%!(BADWIDTH)%!w(BADINDEX)"})
	fmt.Printf("ops: spec=%q ops=%v\n", spec, ops)
}
