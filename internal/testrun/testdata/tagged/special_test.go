//go:build special

package tagged

import "testing"

func TestGated(t *testing.T) {
	t.Log("only built and run under --tagged special")
}
