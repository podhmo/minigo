package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
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

func TestRunREPLLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fib.go")
	src := "package main\nfunc Fib(n int) int {\n\tif n < 2 {\n\t\treturn n\n\t}\n\treturn Fib(n-1) + Fib(n-2)\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(":load " + strconv.Quote(path) + "\nFib(10)\n:load\n:exit\n")
	var out bytes.Buffer
	if err := runREPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"loaded " + path, "55", path + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
}
