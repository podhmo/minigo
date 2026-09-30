package minigo

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestREPL(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	eval := func(line string) any {
		t.Helper()
		v, err := r.EvalLine(ctx, line)
		if err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
		return r.Display(v)
	}

	// a bare expression evaluates and prints
	if diff := cmp.Diff(int64(3), eval("1 + 2")); diff != "" {
		t.Fatalf("1+2 (-want +got):\n%s", diff)
	}

	// := introduces a persistent global
	eval("x := 10")
	if diff := cmp.Diff(int64(20), eval("x * 2")); diff != "" {
		t.Fatalf("x*2 (-want +got):\n%s", diff)
	}

	// var with and without initializer
	eval(`var y = "hi"`)
	if diff := cmp.Diff("hi!", eval(`y + "!"`)); diff != "" {
		t.Fatalf(`y+"!" (-want +got):\n%s`, diff)
	}
	eval("var z int")
	if diff := cmp.Diff(int64(0), eval("z")); diff != "" {
		t.Fatalf("z (-want +got):\n%s", diff)
	}

	// := on an existing name re-assigns it rather than shadowing
	eval("x := 99")
	if diff := cmp.Diff(int64(99), eval("x")); diff != "" {
		t.Fatalf("x (-want +got):\n%s", diff)
	}

	// func declarations persist and can call globals
	eval("func double(n int) int { return n * 2 }")
	if diff := cmp.Diff(int64(198), eval("double(x)")); diff != "" {
		t.Fatalf("double(x) (-want +got):\n%s", diff)
	}

	// type declarations persist
	eval("type Pair struct { A int\nB int }")
	eval("p := Pair{A: 1, B: 2}")
	if diff := cmp.Diff(int64(2), eval("p.B")); diff != "" {
		t.Fatalf("p.B (-want +got):\n%s", diff)
	}

	// imports accumulate for later lines
	eval(`import "strings"`)
	if diff := cmp.Diff("ABC", eval(`strings.ToUpper("abc")`)); diff != "" {
		t.Fatalf("strings.ToUpper (-want +got):\n%s", diff)
	}

	// multi-statement input works; trailing expr is the value
	if diff := cmp.Diff(int64(5), eval("a := 2\nb := 3\na + b")); diff != "" {
		t.Fatalf("a+b (-want +got):\n%s", diff)
	}

	// an error does not corrupt the session
	if _, err := r.EvalLine(ctx, "x +"); err == nil {
		t.Fatal("expected parse error")
	}
	if diff := cmp.Diff(int64(99), eval("x")); diff != "" {
		t.Fatalf("x after error (-want +got):\n%s", diff)
	}

	// Reset clears everything
	r.Reset()
	if _, err := r.EvalLine(ctx, "x"); err == nil {
		t.Fatal("expected undefined name after Reset")
	}
}

func TestREPLRedefinition(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	if _, err := r.EvalLine(ctx, "func f() int { return 1 }"); err != nil {
		t.Fatal(err)
	}
	v, err := r.EvalLine(ctx, "f()")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(1), r.Display(v)); diff != "" {
		t.Fatalf("f() (-want +got):\n%s", diff)
	}
	// redefining must evict the cached materialization
	if _, err := r.EvalLine(ctx, "func f() int { return 2 }"); err != nil {
		t.Fatal(err)
	}
	v, err = r.EvalLine(ctx, "f()")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(2), r.Display(v)); diff != "" {
		t.Fatalf("f() after redefinition (-want +got):\n%s", diff)
	}
}

func TestREPLFailedHoistRollsBack(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	if _, err := r.EvalLine(ctx, "bad := missing"); err == nil {
		t.Fatal("expected undefined-name error")
	}
	// the hoisted cell must be rolled back: `bad` is still undefined
	if _, err := r.EvalLine(ctx, "bad"); err == nil {
		t.Fatal("expected bad to stay undefined after failed initializer")
	}
}

func TestREPLCrossLineDecl(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()
	// const decls are hoisted too
	if _, err := r.EvalLine(ctx, "const k = 42"); err != nil {
		t.Fatal(err)
	}
	v, err := r.EvalLine(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(42), r.Display(v)); diff != "" {
		t.Fatalf("k (-want +got):\n%s", diff)
	}
}

func TestREPLConstAndTypedVar(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()

	evalErr := func(line string) error {
		t.Helper()
		_, err := r.EvalLine(ctx, line)
		return err
	}

	// const bindings are read-only after their initializer runs
	if err := evalErr("const k = 42"); err != nil {
		t.Fatal(err)
	}
	if err := evalErr("k = 5"); err == nil {
		t.Fatal("expected error assigning to const")
	}

	// a typed var keeps its declared constraint on later assignments
	if err := evalErr("var n int"); err != nil {
		t.Fatal(err)
	}
	if err := evalErr(`n = "s"`); err == nil {
		t.Fatal("expected error assigning string to int-typed cell")
	}
	if err := evalErr("n = 7"); err != nil {
		t.Fatal(err)
	}
	v, err := r.EvalLine(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(int64(7), r.Display(v)); diff != "" {
		t.Fatalf("n (-want +got):\n%s", diff)
	}

	// a failed blank import must not poison later lines
	if err := evalErr(`import _ "missing/pkg"`); err == nil {
		t.Fatal("expected error for missing blank import")
	}
	if err := evalErr("after := 1"); err != nil {
		t.Fatalf("line after failed blank import: %v", err)
	}
}

func TestIncompleteInput(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"1 + 2", false},
		{"x := 1", false},
		{"x := f(1)", false},
		{"func f() int { return 1 }", false},
		{"func f() int {", true},
		{"func f() int {\nreturn 1\n}", false},
		{"x := []int{", true},
		{"x := []int{1,\n2,\n}", false},
		{"x := []int{1,\n2,", true},
		{"if x {", true},
		{"if x {\n}\nelse {", true}, // `} else {` only binds inside one buffer
		{"x := (1", true},
		{"x := 1 +", true}, // operator at EOL: no semicolon inserted
		{"x :=", true},
		{"x.", true},
		{"}", false},  // negative depth: an error, not a continuation
		{"x +", true}, // also a parse error, but waits for the operand
		{"// note", false},
		{"", false},
		{"x := `abc", true}, // raw strings legitimately span lines
		{"x := `abc\ndef`", false},
		{"/* note", true}, // so do block comments
		{"/* note\nmore */", false},
		{"x := \"abc", false}, // '"' strings cannot span lines: error now
		{"x := 'a", false},    // rune literals likewise
	}
	for _, c := range cases {
		if got := IncompleteInput(c.src); got != c.want {
			t.Errorf("IncompleteInput(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestREPLSpecialsAndBoundPkgs(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()
	if _, err := r.EvalLine(ctx, `import "fmt"`); err != nil {
		t.Fatal(err)
	}
	// fmt.Println writes via the engine's output writer (discard by default)
	if _, err := r.EvalLine(ctx, `fmt.Sprintln("ok")`); err != nil {
		t.Fatal(err)
	}
	v, err := r.EvalLine(ctx, `strings.TrimSpace("  x  ")`)
	if err == nil {
		t.Fatal("expected error: strings not imported")
	}
	_ = v
}
