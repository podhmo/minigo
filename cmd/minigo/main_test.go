package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunREPLMultiLine(t *testing.T) {
	in := strings.NewReader(strings.Join([]string{
		"func f() int {",
		"return 41 + 1",
		"}",
		"f()",
		"x := []int{",
		"1,",
		"2,",
		"}",
		"x[1]",
		"s := `a", // multi-line raw string literal
		"b`",
		"s",
		"/* hi", // multi-line block comment
		"there */",
		"7",
		":exit",
	}, "\n") + "\n")
	var out bytes.Buffer
	if err := runREPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{".. ", "42", "2", "a\nb", "7"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
}
