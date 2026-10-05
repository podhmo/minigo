package minigo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// writeFiles lays out name -> source under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, src := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// replEval evaluates line and returns the displayed value.
func replEval(t *testing.T, r *REPL, line string) any {
	t.Helper()
	v, err := r.EvalLine(context.Background(), line)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return r.Display(v)
}

// replFails asserts line errors with a message containing want.
func replFails(t *testing.T, r *REPL, line, want string) {
	t.Helper()
	_, err := r.EvalLine(context.Background(), line)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: want error containing %q, got %v", line, want, err)
	}
}

const loadFib = `package main

import (
	"fmt"
	s "strings"
)

const Base = 2

var memo = map[int]int{}
var Calls int

func init() { memo[0], memo[1] = 0, 1 }

type X struct{ N int }

func (x X) Show() string { return fmt.Sprintf("X(%d)", x.N) + s.Repeat("!", 1) }

func Fib(n int) int {
	Calls++
	if v, ok := memo[n]; ok {
		return v
	}
	memo[n] = Fib(n-1) + Fib(n-Base)
	return memo[n]
}

func main() { fmt.Println(Fib(10)) }
`

func TestREPLLoadFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"fib.go": loadFib})
	r := NewEngine(dir).NewREPL()

	paths, err := r.Load(ctx, `"./fib.go"`)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{filepath.Join(dir, "fib.go")}, paths); diff != "" {
		t.Errorf("Load paths (-want +got):\n%s", diff)
	}
	for _, c := range []struct {
		line string
		want any
	}{
		{"len(memo)", int64(2)}, // init() ran at load
		{"Fib(10)", int64(55)},
		{"Base", int64(2)},
		{"X{3}.Show()", "X(3)!"}, // the file's own import alias resolves
	} {
		if diff := cmp.Diff(c.want, replEval(t, r, c.line)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.line, diff)
		}
	}
	// file imports stay file-scoped
	replFails(t, r, `fmt.Sprint(1)`, "undefined: fmt")
	replFails(t, r, `s.Repeat("a", 2)`, "undefined: s")
	// the prompt shares the package block: types extend, methods attach
	replEval(t, r, "type XX X")
	replEval(t, r, "func (x X) Twice() int { return x.N * 2 }")
	if diff := cmp.Diff(int64(8), replEval(t, r, "XX{N: 1}.N + X{N: 2}.Twice() + 3")); diff != "" {
		t.Errorf("prompt extension (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{filepath.Join(dir, "fib.go")}, r.Loaded()); diff != "" {
		t.Errorf("Loaded (-want +got):\n%s", diff)
	}
	lines, err := r.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if l == "func init" {
			t.Errorf("List must hide init: %v", lines)
		}
	}
}

func TestREPLLoadDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		// both files bind x to different packages: legal per-file scopes
		"foo/a.go":       "package foo\nimport x \"strings\"\nvar A = B + 1\nfunc Up(v string) string { return x.ToUpper(v) }\n",
		"foo/b.go":       "package foo\nimport x \"bytes\"\nvar B = 41\nfunc Len(v string) int { return len(x.TrimSpace([]byte(v))) }\n",
		"foo/b_test.go":  "package foo\nfunc Up() {}\n", // excluded like go build
		"foo/sub/sub.go": "package sub\nfunc Hi() string { return \"sub\" }\n",
		"foo/c.go":       "package foo\nimport \"./sub\"\nfunc Call() string { return sub.Hi() }\n",
	})
	r := NewEngine(dir).NewREPL()
	paths, err := r.Load(ctx, "./foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Errorf("Load paths: want a.go b.go c.go, got %v", paths)
	}
	for line, want := range map[string]any{
		"A":            int64(42), // cross-file dependency order
		`Up("hi")`:     "HI",
		`Len("  ab ")`: int64(2),
		"Call()":       "sub", // relative import anchors at the file's dir
	} {
		if diff := cmp.Diff(want, replEval(t, r, line)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", line, diff)
		}
	}
	replFails(t, r, `x.ToUpper("a")`, "undefined: x")
}

func TestREPLLoadShadowAndReload(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"m.go": "package m\nfunc F() int { return 1 }\nfunc G() int { return 10 }\n"})
	r := NewEngine(dir).NewREPL()

	// a prompt decl of the same name is replaced by the load
	replEval(t, r, "func F() int { return 0 }")
	if _, err := r.Load(ctx, "m.go"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(1), replEval(t, r, "F()")); diff != "" {
		t.Errorf("load over prompt (-want +got):\n%s", diff)
	}
	// a failed input does not keep shadowing the file's F
	replFails(t, r, "import _ \"example.com/nosuch\"\nfunc F() int { return 0 }", "nosuch")
	if diff := cmp.Diff(int64(1), replEval(t, r, "F()")); diff != "" {
		t.Errorf("after failed input (-want +got):\n%s", diff)
	}
	// a later prompt redefinition wins until the next load
	replEval(t, r, "func F() int { return 2 }")
	if diff := cmp.Diff(int64(12), replEval(t, r, "F() + G()")); diff != "" {
		t.Errorf("prompt over load (-want +got):\n%s", diff)
	}

	// reloading picks up edits: G removed, F changed
	writeFiles(t, dir, map[string]string{"m.go": "package m\nfunc F() int { return 3 }\n"})
	if _, err := r.Load(ctx, "m.go"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(3), replEval(t, r, "F()")); diff != "" {
		t.Errorf("after reload (-want +got):\n%s", diff)
	}
	replFails(t, r, "G()", "undefined: G")
}

func TestREPLLoadCurrentDirUnexported(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.go": "package main\nvar counter = base * 2\nfunc bump() int { counter++; return counter }\n",
		"b.go": "package main\nconst base = 5\ntype point struct{ x, y int }\nfunc (p point) sum() int { return p.x + p.y }\n",
	})
	r := NewEngine(dir).NewREPL()
	if _, err := r.Load(ctx, `"./"`); err != nil {
		t.Fatal(err)
	}
	// unexported funcs, vars, consts, types and methods are package-block
	// names: the prompt reaches them like any file of the package would
	for _, c := range []struct {
		line string
		want any
	}{
		{"bump()", int64(11)}, // ordered: bump mutates counter
		{"counter", int64(11)},
		{"point{1, 2}.sum()", int64(3)},
		{"base", int64(5)},
	} {
		if diff := cmp.Diff(c.want, replEval(t, r, c.line)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.line, diff)
		}
	}
}

func TestREPLLoadErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"m.go":         "package m\nfunc F() int { return 1 }\n",
		"other/o.go":   "package other\nfunc F() {}\n",
		"twice/a.go":   "package twice\nfunc T() {}\n",
		"twice/b.go":   "package twice\nfunc T() {}\n",
		"same.go":      "package s\nfunc F() {}\nfunc G() {}\nfunc F() {}\n",
		"bad.go":       "package bad\nfunc Oops( {\n",
		"panic.go":     "package p\nfunc init() { panic(\"boom\") }\nfunc P() int { return 1 }\n",
		"notes.txt":    "hi",
		"undef/u.go":   "package u\nvar U = missing()\n",
		"emptydir/x.c": "",
	})
	r := NewEngine(dir).NewREPL()
	if _, err := r.Load(ctx, "m.go"); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{
		"other":     "F in " + filepath.Join(dir, "other/o.go") + " is already declared by :load " + filepath.Join(dir, "m.go"),
		"twice":     "b.go:2:1: T redeclared (previous declaration at " + filepath.Join(dir, "twice/a.go") + ":2:1)",
		"same.go":   "same.go:4:1: F redeclared (previous declaration at " + filepath.Join(dir, "same.go") + ":2:1)",
		"bad.go":    "expected ')'",
		"panic.go":  "boom",
		"notes.txt": "is not a .go file",
		"nosuch.go": "no such file",
		"undef":     "missing",
		"emptydir":  "no buildable Go source files",
	} {
		_, err := r.Load(ctx, ref)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Load(%s): want error containing %q, got %v", ref, want, err)
		}
	}
	// failed loads leave the session as it was
	if diff := cmp.Diff([]string{filepath.Join(dir, "m.go")}, r.Loaded()); diff != "" {
		t.Errorf("Loaded after failures (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(int64(1), replEval(t, r, "F()")); diff != "" {
		t.Errorf("F after failures (-want +got):\n%s", diff)
	}
	replFails(t, r, "P()", "undefined: P")

	r.Reset()
	if len(r.Loaded()) != 0 {
		t.Errorf("Reset must drop loads: %v", r.Loaded())
	}
	replFails(t, r, "F()", "undefined: F")
}

func TestREPLConstRedeclare(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"c.go": "package c\nconst C = 40\n"})
	r := NewEngine(dir).NewREPL()

	replEval(t, r, "const C = 10")
	replFails(t, r, "C = 20", "cannot assign to constant") // assignment stays an error
	if len(r.Warnings()) != 0 {
		t.Errorf("failed assignment must not warn: %v", r.Warnings())
	}
	// redeclaration is a new definition: allowed, with a warning
	replEval(t, r, "const C = 30")
	if diff := cmp.Diff([]string{"const C redeclared (was 10)"}, r.Warnings()); diff != "" {
		t.Errorf("redeclare warnings (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(int64(30), replEval(t, r, "C")); diff != "" {
		t.Errorf("C (-want +got):\n%s", diff)
	}
	// a failing redeclaration keeps the old const and drops the warning
	replFails(t, r, "const C = missing()", "missing")
	if len(r.Warnings()) != 0 {
		t.Errorf("failed redeclaration must not warn: %v", r.Warnings())
	}
	if diff := cmp.Diff(int64(30), replEval(t, r, "C")); diff != "" {
		t.Errorf("C after failed redeclaration (-want +got):\n%s", diff)
	}

	// :load replacing a prompt const warns; re-loading its own does not
	for _, c := range []struct {
		input string
		want  []string
	}{
		{":load", []string{"const C redeclared by c.go (was 30)"}},
		{":load", nil},
		{"const C = 1", []string{"const C redeclared (was 40)"}},
		{":load", []string{"const C redeclared by c.go (was 1)"}},
	} {
		if c.input == ":load" {
			if _, err := r.Load(ctx, "c.go"); err != nil {
				t.Fatal(err)
			}
		} else {
			replEval(t, r, c.input)
		}
		if diff := cmp.Diff(c.want, r.Warnings()); diff != "" {
			t.Errorf("%s warnings (-want +got):\n%s", c.input, diff)
		}
	}
	if diff := cmp.Diff(int64(40), replEval(t, r, "C")); diff != "" {
		t.Errorf("C after load (-want +got):\n%s", diff)
	}
	replFails(t, r, "C = 2", "cannot assign to constant")
}

func TestREPLLoadFileThenDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"pkg/f.go": "package p\nconst K = 1\nfunc F() int { return K }\n",
		"pkg/g.go": "package p\nfunc G() int { return 2 }\n",
	})
	r := NewEngine(dir).NewREPL()
	// f.go and ./pkg/f.go name the same load
	for _, ref := range []string{"pkg/f.go", "./pkg/f.go"} {
		if _, err := r.Load(ctx, ref); err != nil {
			t.Fatalf("Load(%s): %v", ref, err)
		}
	}
	if diff := cmp.Diff([]string{filepath.Join(dir, "pkg/f.go")}, r.Loaded()); diff != "" {
		t.Errorf("Loaded (-want +got):\n%s", diff)
	}
	// the directory absorbs the single-file load instead of clashing on F
	if _, err := r.Load(ctx, "./pkg"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{filepath.Join(dir, "pkg")}, r.Loaded()); diff != "" {
		t.Errorf("Loaded after dir (-want +got):\n%s", diff)
	}
	if len(r.Warnings()) != 0 {
		t.Errorf("absorbing its own const must not warn: %v", r.Warnings())
	}
	if diff := cmp.Diff(int64(3), replEval(t, r, "F() + G()")); diff != "" {
		t.Errorf("F() + G() (-want +got):\n%s", diff)
	}
	// a file of a loaded directory reloads through the directory
	if _, err := r.Load(ctx, "pkg/g.go"); err == nil || !strings.Contains(err.Error(), "is part of :load "+filepath.Join(dir, "pkg")) {
		t.Errorf("Load(pkg/g.go): want part-of error, got %v", err)
	}
	// import paths are not filesystem paths
	if _, err := r.Load(ctx, "strings"); err == nil || !strings.Contains(err.Error(), "use import or :cd") {
		t.Errorf("Load(strings): want import-path hint, got %v", err)
	}
}
