package isolate

import "testing"

var touched bool

func TestAsetsState(t *testing.T) { touched = true }

func TestBexpectsFreshState(t *testing.T) {
	if touched {
		t.Error("package state leaked from TestAsetsState")
	}
}
