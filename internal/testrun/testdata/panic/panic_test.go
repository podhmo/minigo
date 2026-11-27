package panics

import "testing"

func TestBoom(t *testing.T) {
	panic("kaboom")
}

func TestAfter(t *testing.T) {}
