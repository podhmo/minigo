package tagged

import "testing"

func TestAlways(t *testing.T) {
	if N() != 1 {
		t.Error("N() != 1")
	}
}
