package stdfmt

import (
	"fmt"
	"testing"
)

func TestDbgRewrite(t *testing.T) {
	out, ops := rewriteWrapVerbsOps("%[9]w %.*s", []any{-1, "v"}, []string{"%!w(BADINDEX)"}, nil)
	fmt.Printf("-> %q %#v\n", out, ops)
	out, ops = rewriteWrapVerbsOps("%[9]w %*.*s", []any{"x", "y", "abcdef"}, []string{"%!w(BADINDEX)"}, nil)
	fmt.Printf("-> %q %#v\n", out, ops)
}
