package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

// The shebang-compatible prologue is a shell contract, but the CLI side
// it depends on is simple: with `--`, script arguments reach os.Args
// unchanged after the script path.
func TestRunShebangScriptArgs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "script.go")
	src := "///usr/bin/env minigo \"$0\" -- \"$@\"; exit\n" +
		"package main\n\n" +
		"import (\n\t\"os\"\n\t\"strings\"\n)\n\n" +
		"func main() {\n" +
		"\t_ = os.WriteFile(" + strconv.Quote(out) + ", []byte(strings.Join(os.Args, \"\\n\")), 0o644)\n" +
		"}\n"
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{script, "--", "a", "b"}); err != nil {
		t.Fatal(err)
	}
	gotBytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{script, "a", "b"}, "\n")
	if diff := cmp.Diff(want, string(gotBytes)); diff != "" {
		t.Errorf("script os.Args (-want +got):\n%s", diff)
	}
}

// `minigo run a.go b.go` runs the file list as one program.
func TestRunFileList(t *testing.T) {
	dir := filepath.Join("testdata", "multifile")
	files := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go")}
	if err := run(context.Background(), append([]string{}, files...)); err != nil {
		t.Fatal(err)
	}
}
