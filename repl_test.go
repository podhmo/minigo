package minigo

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
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
	if diff := cmp.Diff("3", eval("1 + 2")); diff != "" {
		t.Fatalf("1+2 (-want +got):\n%s", diff)
	}

	// := introduces a persistent global
	eval("x := 10")
	if diff := cmp.Diff("20", eval("x * 2")); diff != "" {
		t.Fatalf("x*2 (-want +got):\n%s", diff)
	}

	// var with and without initializer
	eval(`var y = "hi"`)
	if diff := cmp.Diff("hi!", eval(`y + "!"`)); diff != "" {
		t.Fatalf(`y+"!" (-want +got):\n%s`, diff)
	}
	eval("var z int")
	if diff := cmp.Diff("0", eval("z")); diff != "" {
		t.Fatalf("z (-want +got):\n%s", diff)
	}

	// := on an existing name re-assigns it rather than shadowing
	eval("x := 99")
	if diff := cmp.Diff("99", eval("x")); diff != "" {
		t.Fatalf("x (-want +got):\n%s", diff)
	}

	// func declarations persist and can call globals
	eval("func double(n int) int { return n * 2 }")
	if diff := cmp.Diff("198", eval("double(x)")); diff != "" {
		t.Fatalf("double(x) (-want +got):\n%s", diff)
	}

	// type declarations persist
	eval("type Pair struct { A int\nB int }")
	eval("p := Pair{A: 1, B: 2}")
	if diff := cmp.Diff("2", eval("p.B")); diff != "" {
		t.Fatalf("p.B (-want +got):\n%s", diff)
	}

	// imports accumulate for later lines
	eval(`import "strings"`)
	if diff := cmp.Diff("ABC", eval(`strings.ToUpper("abc")`)); diff != "" {
		t.Fatalf("strings.ToUpper (-want +got):\n%s", diff)
	}

	// multi-statement input works; trailing expr is the value
	if diff := cmp.Diff("5", eval("a := 2\nb := 3\na + b")); diff != "" {
		t.Fatalf("a+b (-want +got):\n%s", diff)
	}

	// an error does not corrupt the session
	if _, err := r.EvalLine(ctx, "x +"); err == nil {
		t.Fatal("expected parse error")
	}
	if diff := cmp.Diff("99", eval("x")); diff != "" {
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
	if diff := cmp.Diff("1", r.Display(v)); diff != "" {
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
	if diff := cmp.Diff("2", r.Display(v)); diff != "" {
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
	if diff := cmp.Diff("42", r.Display(v)); diff != "" {
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
	if diff := cmp.Diff("7", r.Display(v)); diff != "" {
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

// TestREPLDirImport covers directory-form imports — `import "./x"`,
// `import "../x"`, `import "/abs/x"` — which real Go forbids but a
// project-root REPL needs. They anchor to the engine's start directory
// (the same root module resolution uses), resolve eagerly at the import
// line, and bind the package's declared name like Go does.
func TestREPLDirImport(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	eval := func(line string) (any, error) {
		v, err := r.EvalLine(ctx, line)
		if err != nil {
			return nil, err
		}
		return r.Display(v), nil
	}

	// a directory import makes the package usable qualified
	if _, err := r.EvalLine(ctx, `import "./inspectpkg"`); err != nil {
		t.Fatalf("dir import: %v", err)
	}
	if got, err := eval(`inspectpkg.Hello("y")`); err != nil || got != "hello y" {
		t.Fatalf("dir-imported func: %v %v", got, err)
	}
	if got, err := eval(`inspectpkg.Count`); err != nil || got != "3" {
		t.Fatalf("dir-imported var: %v %v", got, err)
	}
	// the binding survives later reloads (each line re-parses the spec)
	if got, err := eval(`inspectpkg.User{Name: "n"}.Greet()`); err != nil || got != "hi n" {
		t.Fatalf("dir-imported method: %v %v", got, err)
	}

	// the declared package name wins over the directory basename
	// (testdata/oddname declares package oddpkg)
	if _, err := r.EvalLine(ctx, `import "./oddname"`); err != nil {
		t.Fatalf("oddname import: %v", err)
	}
	if got, err := eval(`oddpkg.Magic()`); err != nil || got != "7" {
		t.Fatalf("declared-name import: %v %v", got, err)
	}
	if _, err := eval(`oddname.Magic()`); err == nil {
		t.Fatal("the directory basename must not bind")
	}

	// an explicit alias wins over the declared name, like Go
	if _, err := r.EvalLine(ctx, `import odd "./oddname"`); err != nil {
		t.Fatal(err)
	}
	if got, err := eval(`odd.Magic()`); err != nil || got != "7" {
		t.Fatalf("aliased dir import: %v %v", got, err)
	}

	// a dot dir-import exposes exported members unqualified
	if _, err := r.EvalLine(ctx, `import . "./inspectpkg"`); err != nil {
		t.Fatal(err)
	}
	if got, err := eval(`Hello("d")`); err != nil || got != "hello d" {
		t.Fatalf("dot dir import: %v %v", got, err)
	}

	// a missing dir fails at the import line and poisons nothing
	if _, err := r.EvalLine(ctx, `import "./missing"`); err == nil {
		t.Fatal("expected error for a missing dir import")
	}
	if got, err := eval(`inspectpkg.Hello("z")`); err != nil || got != "hello z" {
		t.Fatalf("session after failed dir import: %v %v", got, err)
	}
}

// TestREPLResultEcho pins the value-echo contract: only an input that
// ends in an expression returns a value; declarations and assignments
// evaluate silently. A multi-return call renders as a tuple.
func TestREPLResultEcho(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()

	for _, line := range []string{
		`import "strings"`,
		`x := 1`,
		`x = 2`,
		`func f() int { return 1 }`,
		`type T struct{ V int }`,
		`var y int`,
		`const k = 1`,
	} {
		v, err := r.EvalLine(ctx, line)
		if err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
		if v != nil {
			t.Fatalf("EvalLine(%q) returned %v — declarations must be silent", line, v)
		}
		if d := r.Display(v); d != nil {
			t.Fatalf("EvalLine(%q) displays %v — nothing should echo", line, d)
		}
	}

	// a trailing expression still echoes — including a literal nil
	v, err := r.EvalLine(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("2", r.Display(v)); diff != "" {
		t.Fatalf("x (-want +got):\n%s", diff)
	}
	// a literal nil is an expression too — it echoes its nil spelling
	v, err = r.EvalLine(ctx, "nil")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("<nil>", r.Display(v)); diff != "" {
		t.Fatalf("nil (-want +got):\n%s", diff)
	}
	v, err = r.EvalLine(ctx, `strings.TrimPrefix("foo", "f")`)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("oo", r.Display(v)); diff != "" {
		t.Fatalf("TrimPrefix (-want +got):\n%s", diff)
	}
	// a multi-return call echoes its tuple — (3, <nil>), not a raw box
	if _, err := r.EvalLine(ctx, `import "fmt"`); err != nil {
		t.Fatal(err)
	}
	v, err = r.EvalLine(ctx, `fmt.Println("hi")`)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("(3, <nil>)", r.Display(v)); diff != "" {
		t.Fatalf("Println result (-want +got):\n%s", diff)
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

func TestREPLCdLs(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	r := e.NewREPL()

	eval := func(line string) (any, error) {
		v, err := r.EvalLine(ctx, line)
		if err != nil {
			return nil, err
		}
		return r.Display(v), nil
	}

	// unexported names are not reachable before :cd
	if _, err := eval("hiddenVar"); err == nil {
		t.Fatal("hiddenVar should be undefined before :cd")
	}

	p, err := r.Enter(ctx, "github.com/podhmo/minigo/testdata/inspectpkg")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if got := p.Name; got != "inspectpkg" {
		t.Fatalf("entered %q", got)
	}
	if r.Current() != p {
		t.Fatal("Current() should return the entered package")
	}

	// :ls on the entered package enumerates decls incl. unexported
	lines, err := r.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := map[string]bool{"type User": true, "func Hello": true, "var hiddenVar": true, "method User.Greet": true}
	for _, l := range lines {
		delete(want, l)
	}
	for l := range want {
		t.Errorf(":ls missing %q", l)
	}

	// bare names — including unexported — resolve while inside
	if got, err := eval("hiddenVar"); err != nil || got != "1" {
		t.Fatalf("hiddenVar: %v %v", got, err)
	}
	if got, err := eval(`Hello("y")`); err != nil || got != "hello y" {
		t.Fatalf("Hello: %v %v", got, err)
	}
	if got, err := eval(`User{Name: "n"}.Greet()`); err != nil || got != "hi n" {
		t.Fatalf("User literal + method: %v %v", got, err)
	}

	// after touching members, :ls still lists each decl once (the
	// materialized global must not duplicate the index row)
	lines, err = r.List(ctx, "")
	if err != nil {
		t.Fatalf("List after eval: %v", err)
	}
	counts := map[string]int{}
	for _, l := range lines {
		counts[l]++
	}
	for l, n := range counts {
		if n > 1 {
			t.Errorf(":ls shows %q %d times", l, n)
		}
	}

	// :cd - restores <repl> scope
	if err := r.Leave(); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if r.Current() != nil {
		t.Fatal("Current() should be nil after Leave")
	}
	if _, err := eval("hiddenVar"); err == nil {
		t.Fatal("hiddenVar should be undefined after :cd -")
	}

	// :ls on a bound package lists host pseudo-decls
	lines, err = r.List(ctx, "strings")
	if err != nil {
		t.Fatalf("List(strings): %v", err)
	}
	if len(lines) == 0 || lines[0][:4] != "host" {
		t.Fatalf("bound list: %v", lines)
	}
}

// TestREPLPinWrite exercises :cd write mode (:pin): declarations land in
// the entered package's globals so every importer in the session sees
// the patch, and :unpin decouples the repl scope without undoing them.
func TestREPLPinWrite(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	r := e.NewREPL()

	eval := func(line string) (any, error) {
		v, err := r.EvalLine(ctx, line)
		if err != nil {
			return nil, err
		}
		return r.Display(v), nil
	}
	mustEval := func(line string) any {
		t.Helper()
		v, err := eval(line)
		if err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
		return v
	}
	globalCell := func(p *runtime.Package, name string) *runtime.Cell {
		t.Helper()
		gv, ok := p.Globals.Get(name)
		if !ok {
			t.Fatalf("%s missing from %s globals", name, p.Name)
		}
		c, ok := gv.(*runtime.Cell)
		if !ok {
			t.Fatalf("%s is %T, not a cell", name, gv)
		}
		return c
	}

	// a repl var sharing a package var's name is shadowed by the pin
	// alias — and restored by unpin
	mustEval("hiddenVar := 100")

	// pin needs an entered package
	if err := r.Pin(); err == nil {
		t.Fatal("Pin outside :cd should fail")
	}
	p, err := r.Enter(ctx, "github.com/podhmo/minigo/testdata/inspectpkg")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if err := r.Pin(); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if !r.Pinned() {
		t.Fatal("Pinned() should report write mode")
	}

	// `x = v` writes through the aliased package var cell
	if _, err := eval("hiddenVar = 42"); err != nil {
		t.Fatalf("assign to package var: %v", err)
	}
	if got := globalCell(p, "hiddenVar").Elem; got != int64(42) {
		t.Fatalf("package hiddenVar = %v", got)
	}

	// `x := v` on a package var reuses its cell
	if _, err := eval("hiddenVar := 7"); err != nil {
		t.Fatal(err)
	}
	if got := globalCell(p, "hiddenVar").Elem; got != int64(7) {
		t.Fatalf("package hiddenVar after := = %v", got)
	}

	// a failed := on a package var must not sever the shared cell
	if _, err := eval("hiddenVar := oops()"); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := eval("hiddenVar = 8"); err != nil {
		t.Fatalf("write-through broken after failed input: %v", err)
	}
	if got := globalCell(p, "hiddenVar").Elem; got != int64(8) {
		t.Fatalf("package hiddenVar after failed input = %v", got)
	}

	// new names publish into the package's globals
	mustEval("newvar := 5")
	if got := globalCell(p, "newvar").Elem; got != int64(5) {
		t.Fatalf("package newvar = %v", got)
	}

	// const cells stay read-only through the shared binding
	if _, err := eval(`Label = "x"`); err == nil {
		t.Fatal("expected const-assign trap")
	}

	// func decls patch the package member — engine calls see it too
	mustEval(`func Hello(s string) string { return "patched " + s }`)
	if v, err := r.engine.Call(ctx, p, "Hello", "z"); err != nil || v != "patched z" {
		t.Fatalf("patched Hello via engine: %v %v", v, err)
	}
	if got, err := eval(`Hello("y")`); err != nil || got != "patched y" {
		t.Fatalf("patched Hello via repl: %v %v", got, err)
	}
	// a patch whose body references a package member (another patched
	// decl) resolves through the entered package — before and after Leave
	mustEval(`func Wrap(s string) string { return Hello(s) + "!" }`)
	if v, err := r.engine.Call(ctx, p, "Wrap", "w"); err != nil || v != "patched w!" {
		t.Fatalf("Wrap: %v %v", v, err)
	}

	// repl imports travel with published decls
	if _, err := eval(`import "strings"`); err != nil {
		t.Fatal(err)
	}

	// type decls land in the package and stay usable unqualified
	mustEval("type T2 struct { V int }")
	if _, ok := p.Globals.Get("T2"); !ok {
		t.Fatal("T2 not published")
	}
	if got, err := eval("T2{V: 9}.V"); err != nil || got != "9" {
		t.Fatalf("T2 literal: %v %v", got, err)
	}
	// a method on a repl-declared type grafts onto the published typedef
	mustEval(`func (t T2) M() int { return t.V + 1 }`)
	if got, err := eval("T2{V: 2}.M()"); err != nil || got != "3" {
		t.Fatalf("method on published type: %v %v", got, err)
	}

	// method decls graft onto the entered package's type index; the
	// body resolves repl imports via the grafted file scope
	mustEval(`func (u User) Shout() string { return "!" + strings.ToUpper(u.Name) }`)
	if got, err := eval(`User{Name: "n"}.Shout()`); err != nil || got != "!N" {
		t.Fatalf("patched method: %v %v", got, err)
	}

	// :ls shows the grafted method and the patched decl
	lines, err := r.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := map[string]bool{"method User.Shout": true, "patch T2": true}
	for _, l := range lines {
		delete(want, l)
	}
	for l := range want {
		t.Errorf(":ls missing %q", l)
	}

	// published decls are introspectable through the entered package's
	// index — without registration SymbolOf would read nil or, for a
	// patched name, the shadowed original decl
	if _, err := eval(`import "minigo.dev/inspect"`); err != nil {
		t.Fatal(err)
	}
	if got, err := eval(`inspect.SymbolOf(Wrap).Kind`); err != nil || got != "func" {
		t.Fatalf("SymbolOf(Wrap): %v %v", got, err)
	}
	if got, err := eval(`inspect.Pos(inspect.SymbolOf(Wrap)).File`); err != nil ||
		!strings.HasPrefix(got.(string), "repl.go") {
		t.Fatalf("SymbolOf(Wrap) pos: %v %v", got, err)
	}
	// a patched name reports the repl decl, not the shadowed original
	if got, err := eval(`inspect.Pos(inspect.SymbolOf(Hello)).File`); err != nil ||
		!strings.HasPrefix(got.(string), "repl.go") {
		t.Fatalf("SymbolOf(Hello) pos: %v %v", got, err)
	}
	if got, err := eval(`inspect.SymbolOf(T2).Kind`); err != nil || got != "type" {
		t.Fatalf("SymbolOf(T2): %v %v", got, err)
	}
	// ...and the package's own decl listing sees the patch as a func
	// (before registration it surfaced as a host pseudo-decl)
	if got, err := eval(`inspect.Symbol(inspect.PackageOf("github.com/podhmo/minigo/testdata/inspectpkg"), "Wrap").Kind`); err != nil || got != "func" {
		t.Fatalf("Symbol(Wrap): %v %v", got, err)
	}

	// unpin decouples: writes stay in the package, repl names detach
	r.Unpin()
	if r.Pinned() {
		t.Fatal("unpin should clear write mode")
	}
	if got := globalCell(p, "hiddenVar").Elem; got != int64(8) {
		t.Fatalf("written value lost on unpin: %v", got)
	}
	// the repl binding shadowed by the alias is restored, value intact
	if got, err := eval("hiddenVar"); err != nil || got != "100" {
		t.Fatalf("hiddenVar after unpin: %v %v", got, err)
	}
	// assigns now land in <repl> only — a plain shadow, not a patch
	if _, err := eval("hiddenVar = 99"); err != nil {
		t.Fatal(err)
	}
	if got := globalCell(p, "hiddenVar").Elem; got != int64(8) {
		t.Fatalf("unpinned write leaked into the package: %v", got)
	}
	if got, err := eval("hiddenVar"); err != nil || got != "99" {
		t.Fatalf("repl shadow: %v %v", got, err)
	}
	// a borrowed name (published var) still resolves through the dot-import
	if got, err := eval("newvar"); err != nil || got != "5" {
		t.Fatalf("newvar after unpin: %v %v", got, err)
	}

	// leaving keeps the package patched — including bodies that
	// reference package members or repl-side imports
	if err := r.Leave(); err != nil {
		t.Fatal(err)
	}
	if v, err := r.engine.Call(ctx, p, "Hello", "q"); err != nil || v != "patched q" {
		t.Fatalf("patch survives Leave: %v %v", v, err)
	}
	if v, err := r.engine.Call(ctx, p, "Wrap", "w"); err != nil || v != "patched w!" {
		t.Fatalf("member-referencing patch after Leave: %v %v", v, err)
	}

	// re-entering still sees the grafts; the repl file's import scope
	// registered into the package keeps the method's imports alive
	if _, err := r.Enter(ctx, "github.com/podhmo/minigo/testdata/inspectpkg"); err != nil {
		t.Fatal(err)
	}
	if got, err := eval(`User{Name: "n"}.Shout()`); err != nil || got != "!N" {
		t.Fatalf("patched method after re-enter: %v %v", got, err)
	}
}

func TestREPLImportNameRefs(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	r := e.NewREPL()
	for _, line := range []string{`import foo "encoding/json"`, `import "./testdata/inspectpkg"`, `import "strings"`} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}

	abs, err := filepath.Abs("testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"foo": "encoding/json", "strings": "strings", "inspectpkg": abs} {
		got, ok := r.ImportPathOf(name)
		if !ok || got != want {
			t.Errorf("ImportPathOf(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	if _, ok := r.ImportPathOf("json"); ok {
		t.Error("ImportPathOf(json): the import is aliased to foo")
	}

	// :ls takes an import-bound name or a quoted path like an import spec
	want := []string{"host Marshal", "host MarshalIndent", "host Number", "host Unmarshal", "host Valid"}
	for _, ref := range []string{"foo", `"encoding/json"`} {
		lines, err := r.List(ctx, ref)
		if err != nil {
			t.Fatalf("List(%s): %v", ref, err)
		}
		if diff := cmp.Diff(want, lines); diff != "" {
			t.Errorf("List(%s) (-want +got):\n%s", ref, diff)
		}
	}
	lines, err := r.List(ctx, "inspectpkg")
	if err != nil {
		t.Fatalf("List(inspectpkg): %v", err)
	}
	if !slices.Contains(lines, "func Hello") {
		t.Errorf("List(inspectpkg) missing func Hello: %v", lines)
	}
}

func TestREPLListHoistedKinds(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	for _, line := range []string{"const C = 1", "var v = 2", "x := 3", "const (A = 1; B = 2)"} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	lines, err := r.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"const A", "const B", "const C", "var v", "var x"}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("List (-want +got):\n%s", diff)
	}
}

func TestREPLConstIota(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	for _, line := range []string{
		"const (A = iota; B; C)",
		"const (_ = iota; KB = 1 << (10 * iota); MB)",
		"type W int",
		"const (Sun W = iota; Mon)",
		"const (x, y = iota, iota * 10; z, w)",
		"const (p = 5; q)",
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	for _, c := range []struct {
		expr string
		want any
	}{
		{"A + B*10 + C*100", "210"},
		{"MB", "1048576"},
		{"Mon", "1"},
		{"Mon == W(1)", "true"}, // implicit repetition keeps the spec's type
		{"z + w", "11"},
		{"q", "5"},
	} {
		v, err := r.EvalLine(ctx, c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if diff := cmp.Diff(c.want, r.Display(v)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.expr, diff)
		}
	}
	if _, err := r.EvalLine(ctx, "B = 3"); err == nil || !strings.Contains(err.Error(), "cannot assign to constant") {
		t.Errorf("B = 3: want constant assignment error, got %v", err)
	}
}

func TestREPLImportResolvesEagerly(t *testing.T) {
	ctx := context.Background()
	r := NewEngine(".").NewREPL()

	// an unresolvable path fails the import line, not the first use
	if _, err := r.EvalLine(ctx, `import "nosuch/pkg"`); err == nil || !strings.Contains(err.Error(), `import "nosuch/pkg"`) {
		t.Fatalf("want import error, got %v", err)
	}
	if _, ok := r.ImportPathOf("pkg"); ok {
		t.Error("a failed import must not bind its name")
	}

	// the declared package name binds, not the path's last element
	if _, err := r.EvalLine(ctx, `import "github.com/podhmo/minigo/testdata/oddname"`); err != nil {
		t.Fatal(err)
	}
	v, err := r.EvalLine(ctx, "oddpkg.Magic()")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("7", r.Display(v)); diff != "" {
		t.Errorf("oddpkg.Magic() (-want +got):\n%s", diff)
	}
	var pkgs []string
	for _, c := range r.Complete("odd") {
		if c.Kind == CandPackage {
			pkgs = append(pkgs, c.Name)
		}
	}
	if diff := cmp.Diff([]string{"oddpkg"}, pkgs); diff != "" {
		t.Errorf("package candidates for odd (-want +got):\n%s", diff)
	}
}

func TestREPLResultVars(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	eval := func(line string) any {
		t.Helper()
		v, err := r.EvalLine(ctx, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return r.Display(v)
	}
	for _, c := range []struct {
		line string
		want any
	}{
		{"1 + 2", "3"},
		{"_1 * 10", "30"},
		{`"s"`, "s"},
		{"x := 1", nil},   // no printed value: nothing remembered
		{"_3 + _2", "33"}, // _1="s", _2=30, _3=3
		{`func pair() (int, string) { return 7, "seven" }`, nil},
	} {
		if diff := cmp.Diff(c.want, eval(c.line)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.line, diff)
		}
	}
	eval("pair()")
	if diff := cmp.Diff("seven", eval("_1[1]")); diff != "" { // a multi-value result is a []any
		t.Errorf("_1[1] (-want +got):\n%s", diff)
	}
	// a failing input remembers nothing
	if _, err := r.EvalLine(ctx, "undefinedName"); err == nil {
		t.Fatal("want error")
	}
	if diff := cmp.Diff("seven", eval("_1")); diff != "" {
		t.Errorf("_1 after failure (-want +got):\n%s", diff)
	}
}

func TestREPLDisplay(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	for _, line := range []string{
		`import "fmt"`,
		`type In struct{ A int }`,
		`type T struct{ N int }`,
		`func (t T) String() string { return fmt.Sprintf("T<%d>", t.N) }`,
		`type Out struct { P *In; L []In; M map[string]In; E error; T T }`,
		`var ns []int`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	// %v layout, except a nil slice/map keeps %#v's T(nil) spelling so it
	// reads apart from an empty one
	for _, c := range []struct {
		line string
		want any
	}{
		{"Out{}", "{<nil> []repl.In(nil) map[string]repl.In(nil) <nil> T<0>}"},
		{"Out{L: []In{}, M: map[string]In{}}", "{<nil> [] map[] <nil> T<0>}"},
		{"&In{1}", "&{1}"},
		{"T{5}", "T<5>"},
		{"[]*In{nil}", "[<nil>]"},
		{"ns", "[]int(nil)"},
		{"nil", "<nil>"},
		{`"s"`, "s"},
		{`func pair() (int, error) { return 7, nil }`, nil},
		{"pair()", "(7, <nil>)"},
	} {
		v, err := r.EvalLine(ctx, c.line)
		if err != nil {
			t.Fatalf("%s: %v", c.line, err)
		}
		if diff := cmp.Diff(c.want, r.Display(v)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.line, diff)
		}
	}
}

func TestREPLDump(t *testing.T) {
	ctx := context.Background()
	r := NewEngine("testdata").NewREPL()
	for _, line := range []string{
		`type In struct{ A int }`,
		`type T struct{ N int }`,
		`func (t T) String() string { return "T" }`,
		`type Out struct { S string; P *In; L []In; T T }`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	// Go syntax like %#v: String is not consulted
	for _, c := range []struct {
		line string
		want any
	}{
		{`Out{S: "x"}`, `repl.Out{S:"x", P:(*repl.In)(nil), L:[]repl.In(nil), T:repl.T{N:0}}`},
		{"&In{1}", "&repl.In{A:1}"},
		{"[]int{}", "[]int{}"},
		{`func pair() (int, string) { return 7, "s" }`, nil},
		{"pair()", `(7, "s")`},
	} {
		v, err := r.EvalLine(ctx, c.line)
		if err != nil {
			t.Fatalf("%s: %v", c.line, err)
		}
		if diff := cmp.Diff(c.want, r.Dump(v)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.line, diff)
		}
	}
}
