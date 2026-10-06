package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestMinigoError(t *testing.T) {
	cases := []struct {
		name, stderr, want string
	}{
		{"none", "", ""},
		{"trap", "minigo: runtime trap: undefined: complex128\nTraceback (most recent call first):\nFile \"main.go\", line 3, in main()\n", "runtime trap: undefined: complex128"},
		{"panic", "minigo: panic: assignment to entry in nil map\nTraceback (most recent call first):\n", "panic: assignment to entry in nil map"},
		{"script stderr first", "minigo: from the script\nminigo: panic: boom\n", "panic: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diff := cmp.Diff(c.want, MinigoError(c.stderr)); diff != "" {
				t.Errorf("MinigoError mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
