package minigo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/podhmo/minigo/runtime"
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
		{"len(memo)", "2"}, // init() ran at load
		{"Fib(10)", "55"},
		{"Base", "2"},
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
	if diff := cmp.Diff("8", replEval(t, r, "XX{N: 1}.N + X{N: 2}.Twice() + 3")); diff != "" {
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
		"A":            "42", // cross-file dependency order
		`Up("hi")`:     "HI",
		`Len("  ab ")`: "2",
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
	if diff := cmp.Diff("1", replEval(t, r, "F()")); diff != "" {
		t.Errorf("load over prompt (-want +got):\n%s", diff)
	}
	// a failed input does not keep shadowing the file's F
	replFails(t, r, "import _ \"example.com/nosuch\"\nfunc F() int { return 0 }", "nosuch")
	if diff := cmp.Diff("1", replEval(t, r, "F()")); diff != "" {
		t.Errorf("after failed input (-want +got):\n%s", diff)
	}
	// a later prompt redefinition wins until the next load
	replEval(t, r, "func F() int { return 2 }")
	if diff := cmp.Diff("12", replEval(t, r, "F() + G()")); diff != "" {
		t.Errorf("prompt over load (-want +got):\n%s", diff)
	}

	// reloading picks up edits: G removed, F changed
	writeFiles(t, dir, map[string]string{"m.go": "package m\nfunc F() int { return 3 }\n"})
	if _, err := r.Load(ctx, "m.go"); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("3", replEval(t, r, "F()")); diff != "" {
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
		{"bump()", "11"}, // ordered: bump mutates counter
		{"counter", "11"},
		{"point{1, 2}.sum()", "3"},
		{"base", "5"},
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
	if diff := cmp.Diff("1", replEval(t, r, "F()")); diff != "" {
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
	if diff := cmp.Diff("30", replEval(t, r, "C")); diff != "" {
		t.Errorf("C (-want +got):\n%s", diff)
	}
	// a failing redeclaration keeps the old const and drops the warning
	replFails(t, r, "const C = missing()", "missing")
	if len(r.Warnings()) != 0 {
		t.Errorf("failed redeclaration must not warn: %v", r.Warnings())
	}
	if diff := cmp.Diff("30", replEval(t, r, "C")); diff != "" {
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
	if diff := cmp.Diff("40", replEval(t, r, "C")); diff != "" {
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
	if diff := cmp.Diff("3", replEval(t, r, "F() + G()")); diff != "" {
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

// Regression tests from the stack review: each failing input or load must
// leave the session as it was, and the newest definition wins both ways.
func TestREPLReviewRegressions(t *testing.T) {
	ctx := context.Background()

	t.Run("failed decl input keeps a same-named prompt var", func(t *testing.T) {
		r := NewEngine(t.TempDir()).NewREPL()
		replEval(t, r, "G := 1")
		replFails(t, r, "func G() {}\nvar z Nope", "Nope")
		if diff := cmp.Diff("1", replEval(t, r, "G")); diff != "" {
			t.Errorf("G (-want +got):\n%s", diff)
		}
	})

	t.Run("failed re-load keeps the previous load", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"w.go": "package w\nvar W = 5\nfunc F() int { return W }\n"})
		r := NewEngine(dir).NewREPL()
		if _, err := r.Load(ctx, "w.go"); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, dir, map[string]string{"w.go": "package w\nvar X = boom()\nfunc boom() int { panic(\"boom\") }\nfunc F() int { return 0 }\n"})
		if _, err := r.Load(ctx, "w.go"); err == nil {
			t.Fatal("want init failure")
		}
		if diff := cmp.Diff("10", replEval(t, r, "W + F()")); diff != "" {
			t.Errorf("W + F() (-want +got):\n%s", diff)
		}
		replFails(t, r, "X", "undefined: X")
	})

	t.Run("failed load keeps colliding prompt const and var", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"d.go": "package d\nconst C = 2\nvar V = 8\nvar X = boom()\nfunc boom() int { panic(\"boom\") }\n"})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "const C = 1")
		replEval(t, r, "V := 7")
		if _, err := r.Load(ctx, "d.go"); err == nil {
			t.Fatal("want init failure")
		}
		if diff := cmp.Diff("8", replEval(t, r, "C + V")); diff != "" {
			t.Errorf("C + V (-want +got):\n%s", diff)
		}
		if len(r.Warnings()) != 0 {
			t.Errorf("failed load must not warn: %v", r.Warnings())
		}
	})

	t.Run("init runs once when load is the first input", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"i.go": "package i\nvar hits int\nvar N = next()\nfunc next() int { hits++; return hits }\nfunc init() { hits += 10 }\n"})
		r := NewEngine(dir).NewREPL()
		if _, err := r.Load(ctx, "i.go"); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("11", replEval(t, r, "hits")); diff != "" {
			t.Errorf("hits (-want +got):\n%s", diff)
		}
	})

	t.Run("newest definition wins between prompt values and loaded decls", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"h.go": "package h\nfunc H() int { return 1 }\n",
			"v.go": "package v\nvar V = 10\n",
		})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "H := 7")
		if _, err := r.Load(ctx, "h.go"); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("1", replEval(t, r, "H()")); diff != "" {
			t.Errorf("loaded func over prompt var (-want +got):\n%s", diff)
		}
		if _, err := r.Load(ctx, "v.go"); err != nil {
			t.Fatal(err)
		}
		replEval(t, r, "func V() int { return 2 }")
		if diff := cmp.Diff("2", replEval(t, r, "V()")); diff != "" {
			t.Errorf("prompt func over loaded var (-want +got):\n%s", diff)
		}
		// the next load restores the file's var
		if _, err := r.Load(ctx, "v.go"); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("10", replEval(t, r, "V")); diff != "" {
			t.Errorf("V after re-load (-want +got):\n%s", diff)
		}
	})

	t.Run("pin: load redefines a published decl; const redeclaration warns", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"pk/pk.go": "package pk\nconst K = 1\nfunc G() int { return K }\nfunc F() int { return 1 }\n",
			"ld/f.go":  "package ld\nfunc F() int { return 99 }\n",
		})
		r := NewEngine(dir).NewREPL()
		if _, err := r.Enter(ctx, "./pk"); err != nil {
			t.Fatal(err)
		}
		if err := r.Pin(); err != nil {
			t.Fatal(err)
		}
		replEval(t, r, "func F() int { return 2 }")
		if _, err := r.Load(ctx, "ld/f.go"); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("99", replEval(t, r, "F()")); diff != "" {
			t.Errorf("F after load under pin (-want +got):\n%s", diff)
		}
		replEval(t, r, "const K = 5")
		if diff := cmp.Diff([]string{"const K redeclared in package " + r.Current().Path + " (was 1): every importer sees the new value"}, r.Warnings()); diff != "" {
			t.Errorf("pinned const warning (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff("5", replEval(t, r, "G()")); diff != "" {
			t.Errorf("G sees the patched const (-want +got):\n%s", diff)
		}
	})

	t.Run("dir and file overlap follows the dir's file list", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"dd/a.go":      "package dd\nfunc A() int { return 1 }\n",
			"dd/a_test.go": "package dd\nfunc T1() int { return 2 }\n",
		})
		r := NewEngine(dir).NewREPL()
		if _, err := r.Load(ctx, "dd/a_test.go"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Load(ctx, "dd"); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff("3", replEval(t, r, "A() + T1()")); diff != "" {
			t.Errorf("A() + T1() (-want +got):\n%s", diff)
		}
		if _, err := r.Load(ctx, "dd/a_test.go"); err != nil {
			t.Errorf("a file outside the dir's file list loads on its own: %v", err)
		}
	})
}

func TestREPLImportBoundVersionedPath(t *testing.T) {
	e := NewEngine(t.TempDir())
	e.Bind("example.com/foo/v2", map[string]runtime.Value{"X": int64(3)})
	r := e.NewREPL()
	replEval(t, r, `import "example.com/foo/v2"`)
	if diff := cmp.Diff("3", replEval(t, r, "foo.X")); diff != "" {
		t.Errorf("foo.X (-want +got):\n%s", diff)
	}
}

func TestREPLLoadTakeOver(t *testing.T) {
	ctx := context.Background()
	load := func(t *testing.T, r *REPL, ref string) {
		t.Helper()
		if _, err := r.Load(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("reload without a name brings the prompt definition back", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"m.go": "package m\nvar V = 10\nfunc F() int { return 1 }\n"})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "V := 7")
		replEval(t, r, "func F() int { return 0 }")
		load(t, r, "m.go")
		if diff := cmp.Diff("11", replEval(t, r, "V + F()")); diff != "" {
			t.Errorf("loaded (-want +got):\n%s", diff)
		}
		writeFiles(t, dir, map[string]string{"m.go": "package m\nfunc G() int { return 2 }\n"})
		load(t, r, "m.go")
		if diff := cmp.Diff("9", replEval(t, r, "V + F() + G()")); diff != "" {
			t.Errorf("reloaded (-want +got):\n%s", diff)
		}
	})

	t.Run("a still-declared name keeps the prompt definition for later", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"m.go": "package m\nvar V = 10\n"})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "V := 7")
		load(t, r, "m.go")
		writeFiles(t, dir, map[string]string{"m.go": "package m\nvar V = 20\n"})
		load(t, r, "m.go")
		if diff := cmp.Diff("20", replEval(t, r, "V")); diff != "" {
			t.Errorf("V after reload (-want +got):\n%s", diff)
		}
		writeFiles(t, dir, map[string]string{"m.go": "package m\n"})
		load(t, r, "m.go")
		if diff := cmp.Diff("7", replEval(t, r, "V")); diff != "" {
			t.Errorf("V after removal (-want +got):\n%s", diff)
		}
	})

	t.Run("a newer prompt definition is not clobbered", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"m.go": "package m\nfunc F() int { return 1 }\n"})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "func F() int { return 0 }")
		load(t, r, "m.go")
		replEval(t, r, "func F() int { return 2 }")
		writeFiles(t, dir, map[string]string{"m.go": "package m\n"})
		load(t, r, "m.go")
		if diff := cmp.Diff("2", replEval(t, r, "F()")); diff != "" {
			t.Errorf("F (-want +got):\n%s", diff)
		}
	})

	t.Run("a grouped prompt type loses only the loaded spec", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"m.go": "package m\ntype A struct{ N int }\n"})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "type (\n\tA int\n\tB string\n)")
		load(t, r, "m.go")
		if diff := cmp.Diff("4", replEval(t, r, `A{N: 3}.N + len(B("b"))`)); diff != "" {
			t.Errorf("after load (-want +got):\n%s", diff)
		}
		writeFiles(t, dir, map[string]string{"m.go": "package m\n"})
		load(t, r, "m.go")
		if diff := cmp.Diff("4", replEval(t, r, `int(A(3)) + len(B("b"))`)); diff != "" {
			t.Errorf("after removal (-want +got):\n%s", diff)
		}
	})

	t.Run("a prompt takeover blanks one name and keeps the group's iota", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"m.go": "package m\nconst (\n\tA = iota\n\tB\n\tC\n)\nvar a, b = 1, 2\n"})
		r := NewEngine(dir).NewREPL()
		load(t, r, "m.go")
		replEval(t, r, "type A struct{}")
		replEval(t, r, "func a() int { return 5 }")
		if diff := cmp.Diff("10", replEval(t, r, "B + C + b + a()")); diff != "" {
			t.Errorf("values (-want +got):\n%s", diff)
		}
		ix := r.pkg.Index
		if c, ok := ix.Consts["C"]; !ok || c.Idx != 2 {
			t.Errorf("C must keep spec index 2: %+v", c)
		}
		if _, ok := ix.Vars["b"]; !ok {
			t.Errorf("b must stay declared")
		}
	})

	t.Run("unload drops the load and restores the prompt", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"m.go":   "package m\nvar V = 10\nfunc F() int { return V }\n",
			"d/a.go": "package d\nfunc D() int { return 4 }\n",
		})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "V := 7")
		load(t, r, "m.go")
		load(t, r, "d")
		if _, err := r.Unload(ctx, "nosuch.go"); err == nil || !strings.Contains(err.Error(), "not loaded") {
			t.Errorf("want not loaded, got %v", err)
		}
		if _, err := r.Unload(ctx, "d/a.go"); err == nil {
			t.Error("a file of a loaded dir must not unload alone")
		}
		path, err := r.Unload(ctx, `"./m.go"`)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(filepath.Join(dir, "m.go"), path); diff != "" {
			t.Errorf("path (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff("11", replEval(t, r, "V + D()")); diff != "" {
			t.Errorf("after unload (-want +got):\n%s", diff)
		}
		replFails(t, r, "F()", "undefined: F")
		if diff := cmp.Diff([]string{filepath.Join(dir, "d")}, r.Loaded()); diff != "" {
			t.Errorf("Loaded (-want +got):\n%s", diff)
		}
	})

	t.Run("ls names the file a loaded decl came from", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"d/a.go": "package d\ntype T struct{}\nfunc (T) M() {}\n",
			"d/b.go": "package d\nvar V = 1\n",
		})
		r := NewEngine(dir).NewREPL()
		replEval(t, r, "func P() {}")
		load(t, r, "d")
		lines, err := r.List(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"func P", "method T.M (d/a.go)", "type T (d/a.go)", "var V (d/b.go)"}
		if diff := cmp.Diff(want, lines); diff != "" {
			t.Errorf("List (-want +got):\n%s", diff)
		}
	})
}

func TestREPLCompleteCommandArg(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"fib.go":         "package main\nfunc Fib() {}\n",
		"fizz.go":        "package main\nfunc Fizz() {}\n",
		"notes.txt":      "",
		"fixtures/a.go":  "package fixtures\n",
		".hidden/b.go":   "package hidden\n",
		"pkg/sub/c.go":   "package sub\n",
		"pkg/sub/c2.txt": "",
	})
	r := NewEngine(dir).NewREPL()
	for _, c := range []struct {
		line      string
		wantStart int
		want      []string
	}{
		{":load ", 6, []string{"fib.go", "fixtures/", "fizz.go", "pkg/"}},
		{":load fi", 6, []string{"fib.go", "fixtures/", "fizz.go"}},
		{":load ./fiz", 6, []string{"./fizz.go"}},
		{`:load "./fiz`, 7, []string{"./fizz.go"}},
		{":load pkg/sub/", 6, []string{"pkg/sub/c.go"}},
		{":load .", 6, []string{".hidden/"}},
		{":load " + dir + "/fib", 6, []string{dir + "/fib.go"}},
		{`:load "fib.go" `, len(`:load "fib.go" `), nil},
		{":load nosuch/", 6, nil},
		{":reset x", len(":reset x"), nil},
		{":unload ", 8, nil},
	} {
		start, cands := r.CompleteCommandArg(c.line)
		if diff := cmp.Diff(c.want, candNames(cands), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%q (-want +got):\n%s", c.line, diff)
		}
		if start != c.wantStart {
			t.Errorf("%q: start = %d, want %d", c.line, start, c.wantStart)
		}
	}

	for _, ref := range []string{"fib.go", "pkg/sub"} {
		if _, err := r.Load(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		line string
		want []string
	}{
		{":unload ", []string{"fib.go", "pkg/sub"}},
		{":unload p", []string{"pkg/sub"}},
		{":unload ./", []string{"./fib.go", "./pkg/sub"}},
		{":unload /", []string{filepath.Join(dir, "fib.go"), filepath.Join(dir, "pkg/sub")}},
	} {
		_, cands := r.CompleteCommandArg(c.line)
		if diff := cmp.Diff(c.want, candNames(cands)); diff != "" {
			t.Errorf("%q (-want +got):\n%s", c.line, diff)
		}
	}
}
