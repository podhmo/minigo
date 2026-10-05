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

func TestRunREPLBindings(t *testing.T) {
	in := strings.NewReader(":bindings encoding/\n:exit\n")
	var out bytes.Buffer
	if err := runREPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "encoding/json\n") {
		t.Errorf("output missing encoding/json\n---\n%s", got)
	}
	for _, unwanted := range []string{"fmt\n", "encoding/base32\n"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("output must not contain %q\n---\n%s", unwanted, got)
		}
	}
}
