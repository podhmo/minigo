package main

import (
	goruntime "runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestExampleOutputEqual pins go test's processRunResult comparison:
// whole-string TrimSpace only, \r\n normalized on windows, unordered
// compares sorted line orderings. The per-line trailing-space row is
// the case a line-wise trim would false-pass.
func TestExampleOutputEqual(t *testing.T) {
	tests := []struct {
		name      string
		got       string
		want      string
		unordered bool
		equal     bool
	}{
		{name: "identical", got: "foo\nbar\n", want: "foo\nbar", equal: true},
		{name: "whole string trim", got: "  foo\nbar  ", want: "foo\nbar", equal: true},
		{name: "empty both sides", got: "", want: "", equal: true},
		{name: "empty vs output", got: "", want: "x", equal: false},
		// upstream only trims the whole string: a trailing space on an
		// inner line is a real diff.
		{name: "inner line trailing space", got: "foo \nbar", want: "foo\nbar", equal: false},
		{name: "want line trailing space", got: "foo\nbar", want: "foo\nbar ", equal: true},
		{name: "want inner trailing space", got: "foo\nbar", want: "foo \nbar", equal: false},
		{name: "ordered same lines", got: "a\nb", want: "a\nb", equal: true},
		{name: "ordered different order", got: "b\na", want: "a\nb", equal: false},
		{name: "unordered sorted", got: "b\na", want: "a\nb", unordered: true, equal: true},
		{name: "unordered dup lines", got: "a\na\nb", want: "a\nb\na", unordered: true, equal: true},
		{name: "unordered count mismatch", got: "a\nb", want: "a\nb\nb", unordered: true, equal: false},
		{name: "ordered newline only", got: "x\n\n", want: "x", equal: true},
		// the \r\n normalization is gated on the test binary's OS.
		{name: "crlf got", got: "a\r\nb", want: "a\nb", equal: goruntime.GOOS == "windows"},
		{name: "crlf want", got: "a\nb", want: "a\r\nb", equal: goruntime.GOOS == "windows"},
		{name: "crlf unordered", got: "b\r\na", want: "a\nb", unordered: true, equal: goruntime.GOOS == "windows"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := exampleOutputEqual(tc.got, tc.want, tc.unordered)
			if diff := cmp.Diff(tc.equal, got); diff != "" {
				t.Errorf("exampleOutputEqual(%q, %q, %v) mismatch (-want +got):\n%s", tc.got, tc.want, tc.unordered, diff)
			}
		})
	}
}
