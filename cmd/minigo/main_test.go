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

func TestRunREPLPrintGoSyntax(t *testing.T) {
	in := strings.NewReader("type P struct{ S string; L []int }\n:dump P{S: \"x\"}\n:p\n:p _1\n_1\n:exit\n")
	var out bytes.Buffer
	if err := runREPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// :dump, then its :p alias on the remembered result
	if n := strings.Count(got, `repl.P{S:"x", L:[]int(nil)}`+"\n"); n != 2 {
		t.Errorf("Go-syntax lines = %d, want 2\n---\n%s", n, got)
	}
	for _, want := range []string{
		"usage: :dump <expr>\n",
		"{x []int(nil)}\n", // :p results are remembered like any other
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
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
	in := strings.NewReader(":load " + strconv.Quote(path) + "\nFib(10)\n:load\n:unload " + strconv.Quote(path) + "\nFib(10)\n:unload " + strconv.Quote(path) + "\n:exit\n")
	var out bytes.Buffer
	if err := runREPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"loaded " + path, "55", path + "\n", "unloaded " + path, "undefined: Fib", "is not loaded"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
}
