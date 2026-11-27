package fail

import "testing"

func TestFail(t *testing.T) {
	t.Error("this one fails")
}

func TestOK(t *testing.T) {
	if F() != 42 {
		t.Fatal("F() != 42")
	}
}
