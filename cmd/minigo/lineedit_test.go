package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/podhmo/minigo"
)

func testREPL(t *testing.T) *minigo.REPL {
	t.Helper()
	e := minigo.NewEngine("../../testdata")
	r := e.NewREPL()
	for _, line := range []string{
		`import "./inspectpkg"`,
		`var u inspectpkg.User`,
	} {
		if _, err := r.EvalLine(context.Background(), line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	return r
}

func TestCompleteSpliceSingle(t *testing.T) {
	r := testREPL(t)
	// one candidate: the full name is spliced in
	newLine, newPos, cands, ok := completeSplice(r, "u.Gre", 5)
	if !ok {
		t.Fatal("no splice for u.Gre")
	}
	if newLine != "u.Greet" || newPos != 7 {
		t.Fatalf("u.Gre -> %q pos=%d", newLine, newPos)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
}

func TestCompleteSpliceAmbiguous(t *testing.T) {
	r := testREPL(t)
	// many candidates share the common extension only
	newLine, newPos, cands, ok := completeSplice(r, "u.", 2)
	if !ok {
		t.Fatal("no splice for u.")
	}
	// User members share no common prefix — the line is untouched
	if newLine != "u." || newPos != 2 {
		t.Fatalf("u. -> %q pos=%d", newLine, newPos)
	}
	if len(cands) < 5 {
		t.Fatalf("want the member list, got %d", len(cands))
	}
}

func TestCompleteSplicePreservesTail(t *testing.T) {
	r := testREPL(t)
	// completing mid-line keeps the text after the cursor
	newLine, newPos, _, ok := completeSplice(r, "println(u.Gre)", 13)
	if !ok {
		t.Fatal("no splice mid-line")
	}
	if newLine != "println(u.Greet)" || newPos != 15 {
		t.Fatalf("mid-line -> %q pos=%d", newLine, newPos)
	}
}

func TestCompleteSpliceImport(t *testing.T) {
	r := testREPL(t)
	// testdata has two dirs sharing the prefix — only the common
	// extension goes in
	newLine, _, cands, ok := completeSplice(r, `import "./insp`, 14)
	if !ok {
		t.Fatal("no splice for import path")
	}
	if newLine != `import "./inspect` {
		t.Fatalf("import path -> %q", newLine)
	}
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %v", candidateNames(cands))
	}
	newLine, _, _, ok = completeSplice(r, `import "./inspectp`, 18)
	if !ok || newLine != `import "./inspectpkg` {
		t.Fatalf("import path p -> %q ok=%v", newLine, ok)
	}
}

func TestCompleteSpliceCommand(t *testing.T) {
	r := testREPL(t)
	newLine, _, _, ok := completeSplice(r, ":pi", 3)
	if !ok {
		t.Fatal("no splice for :pi")
	}
	if newLine != ":pin" {
		t.Fatalf(":pi -> %q", newLine)
	}
	// inside the argument position the command table no longer applies —
	// it falls through to the processor completer
	if _, _, _, ok := completeSplice(r, ":ls u.", 6); ok {
		t.Fatal(":ls u. unexpectedly spliced as a command")
	}
}

func TestCompleteSpliceEmpty(t *testing.T) {
	r := testREPL(t)
	// an unresolvable selector yields no candidates and no rewrite
	if _, _, _, ok := completeSplice(r, "nosuch.", 7); ok {
		t.Fatal("nosuch. unexpectedly spliced")
	}
}

func TestCompleteSpliceWhitespace(t *testing.T) {
	r := testREPL(t)
	// `var |x`: the cursor sits on a fresh empty token — tab must not
	// pull the space into the replacement (`varx`).
	newLine, _, _, ok := completeSplice(r, "var x", 4)
	if !ok {
		t.Fatal("no splice on an empty token")
	}
	if newLine != "var x" {
		t.Fatalf("space eaten: %q", newLine)
	}
	// a closed import literal offers nothing — the closing quote is
	// not part of the token.
	if _, _, _, ok := completeSplice(r, `import "strings"`, 16); ok {
		t.Fatal("closed import literal unexpectedly spliced")
	}
}

func TestCommonPrefixRunes(t *testing.T) {
	// the shared cut must land on a rune boundary — a byte-wise walk
	// leaves a dangling first byte when the prefix ends mid-rune.
	if got := commonPrefix([]string{"日本", "日曜"}); got != "日" {
		t.Fatalf("commonPrefix(日本,日曜) = %q", got)
	}
	if got := commonPrefix([]string{"日本", "日本語"}); got != "日本" {
		t.Fatalf("commonPrefix(日本,日本語) = %q", got)
	}
	if got := commonPrefix([]string{"abc", "abd"}); got != "ab" {
		t.Fatalf("commonPrefix(abc,abd) = %q", got)
	}
}

func TestWriteWithCRLF(t *testing.T) {
	var b bytes.Buffer
	w := writeWithCRLF{&b}
	n, err := w.Write([]byte("a\nb\n\nc"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("consumed %d, want 6", n)
	}
	if b.String() != "a\r\nb\r\n\r\nc" {
		t.Fatalf("got %q", b.String())
	}
}

func TestFormatCandidates(t *testing.T) {
	cands := []minigo.Candidate{
		{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"},
	}
	got := formatCandidates(cands)
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s missing in %q", want, got)
		}
	}
}
