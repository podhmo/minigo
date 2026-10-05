package minigo

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

func candNames(cands []Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Name
	}
	sort.Strings(out)
	return out
}

func hasCand(cands []Candidate, name string) bool {
	for _, c := range cands {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestCompleteBareNames(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	// builtins and keywords are in scope before anything is declared
	if !hasCand(r.Complete("printl"), "println") {
		t.Fatalf("println missing: %v", candNames(r.Complete("printl")))
	}
	if !hasCand(r.Complete("fu"), "func") {
		t.Fatalf("func keyword missing: %v", candNames(r.Complete("fu")))
	}
	if !hasCand(r.Complete(""), "string") {
		t.Fatalf("builtin type missing: %v", candNames(r.Complete(""))[:20])
	}

	if _, err := r.EvalLine(ctx, "x := 42"); err != nil {
		t.Fatalf("EvalLine: %v", err)
	}
	cands := r.Complete("x")
	if !hasCand(cands, "x") {
		t.Fatalf("x missing: %v", candNames(cands))
	}
	var xc Candidate
	for _, c := range cands {
		if c.Name == "x" {
			xc = c
		}
	}
	if xc.Kind != CandVar {
		t.Fatalf("x kind = %q, want var", xc.Kind)
	}
}

func TestCompletePackage(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	if _, err := r.EvalLine(ctx, `import "strings"`); err != nil {
		t.Fatalf("EvalLine: %v", err)
	}
	cands := r.Complete("strings.")
	for _, want := range []string{"Builder", "Contains", "NewReader", "TrimSpace"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in strings. -> %v", want, candNames(cands))
		}
	}
	// prefix filtering
	cands = r.Complete("strings.New")
	for _, c := range cands {
		if c.Name[:3] != "New" {
			t.Fatalf("unfiltered candidate %q", c.Name)
		}
	}
	if !hasCand(cands, "NewReader") {
		t.Fatalf("NewReader missing: %v", candNames(cands))
	}
}

func TestCompleteStructAndMethods(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`type Pair struct { A int; B string }`,
		`func (p Pair) Sum() string { return p.B }`,
		`var p Pair`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	cands := r.Complete("p.")
	for _, want := range []string{"A", "B", "Sum"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in p. -> %v", want, candNames(cands))
		}
	}
	// the type name itself selects its methods too (method expressions)
	cands = r.Complete("Pair.")
	if !hasCand(cands, "Sum") {
		t.Fatalf("Sum missing in Pair. -> %v", candNames(cands))
	}
	if hasCand(cands, "A") {
		t.Fatalf("field A unexpectedly in Pair. -> %v", candNames(cands))
	}
}

func TestCompleteNamedBasic(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`type MyInt int`,
		`func (m MyInt) Double() int { return int(m) * 2 }`,
		`var m MyInt`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	if !hasCand(r.Complete("m."), "Double") {
		t.Fatalf("Double missing in m. -> %v", candNames(r.Complete("m.")))
	}
	if !hasCand(r.Complete("MyInt."), "Double") {
		t.Fatalf("Double missing in MyInt. -> %v", candNames(r.Complete("MyInt.")))
	}
}

func TestCompleteEnumMembers(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`type Color int`,
		`const Red Color = 1`,
		`const Blue Color = 2`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	cands := r.Complete("Color.")
	for _, want := range []string{"Red", "Blue"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in Color. -> %v", want, candNames(cands))
		}
	}

	// a var stamped with the typedef is not a type member — enum
	// candidates come from read-only const cells only.
	if _, err := r.EvalLine(ctx, `var Cur Color`); err != nil {
		t.Fatalf("EvalLine: %v", err)
	}
	if hasCand(r.Complete("Color."), "Cur") {
		t.Fatalf("var leaked into Color. -> %v", candNames(r.Complete("Color.")))
	}
}

func TestCompleteDefinedType(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`type Base struct { X int }`,
		`func (b Base) M() int { return b.X }`,
		`type Copy Base`,
		`func (c Copy) Own() int { return 1 }`,
		`var c2 Copy = Copy{9}`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	// `type A B` shares B's storage: the inner struct's decl answers the
	// field layout, but its method set is not inherited — c2.M traps.
	cands := r.Complete("c2.")
	for _, want := range []string{"X", "Own"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in c2. -> %v", want, candNames(cands))
		}
	}
	if hasCand(cands, "M") {
		t.Fatalf("inherited M leaked into c2. -> %v", candNames(cands))
	}
	cands = r.Complete("Copy.")
	if !hasCand(cands, "Own") {
		t.Fatalf("Own missing in Copy. -> %v", candNames(cands))
	}
	if hasCand(cands, "M") {
		t.Fatalf("inherited M leaked into Copy. -> %v", candNames(cands))
	}
}

func TestCompleteNamedSliceElement(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`import "./inspectpkg"`,
		`type Users []inspectpkg.User`,
		`type W struct { Items Users }`,
		`u := inspectpkg.User{}`,
		`w := W{Items: Users{u}}`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	// a field of `type T []E` stores a Named-wrapped Slice — indexing
	// unwraps through the tag to reach the elements.
	cands := r.Complete("w.Items[0].")
	for _, want := range []string{"Name", "Greet"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in w.Items[0]. -> %v", want, candNames(cands))
		}
	}
}

func TestCompleteMethodExprValueSet(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`type Pair struct { A int; B string }`,
		`func (p Pair) Sum() string { return p.B }`,
		`func (p *Pair) SetB(s string) { p.B = s }`,
		`var p Pair`,
		`var pp *Pair`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	// addressable variables take the whole method set…
	cands := r.Complete("p.")
	for _, want := range []string{"Sum", "SetB"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in p. -> %v", want, candNames(cands))
		}
	}
	cands = r.Complete("pp.")
	for _, want := range []string{"Sum", "SetB"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in pp. -> %v", want, candNames(cands))
		}
	}
	// …but `T.M` is a method expression: pointer receivers trap, so the
	// type-level selector keeps only the value method set.
	cands = r.Complete("Pair.")
	if !hasCand(cands, "Sum") {
		t.Fatalf("Sum missing in Pair. -> %v", candNames(cands))
	}
	if hasCand(cands, "SetB") {
		t.Fatalf("ptr-receiver SetB leaked into Pair. -> %v", candNames(cands))
	}
}

func TestCompleteTokenStart(t *testing.T) {
	e := NewEngine("testdata")
	r := e.NewREPL()
	for _, tc := range []struct {
		line  string
		start int
	}{
		{"pri", 0},         // bare ident: replace the whole tail
		{"x.pri", 2},       // selector prefix: replace after the dot
		{"x.", 2},          // dot tail: insert after the dot
		{`import "str`, 8}, // inside the import literal
		{"f(", 2},          // no token: pure insertion point
		{":comp x", 7},     // meta lines are not completed here
		{"var ", 4},        // trailing space: fresh empty token at the cursor
		{"x. ", 3},         // space after the dot: fresh empty token
	} {
		start, _ := r.CompleteToken(tc.line)
		if start != tc.start {
			t.Fatalf("CompleteToken(%q).start = %d, want %d", tc.line, start, tc.start)
		}
	}
}

func TestCompleteClosedImportLiteral(t *testing.T) {
	e := NewEngine("testdata")
	r := e.NewREPL()

	// a closed literal is past the import context — offering paths
	// here would splice over the closing quote.
	start, cands := r.CompleteToken(`import "strings"`)
	if len(cands) != 0 {
		t.Fatalf("closed import literal yielded %v", candNames(cands)[:5])
	}
	if start != len(`import "strings"`) {
		t.Fatalf("start = %d", start)
	}
	// still-open literals complete as before
	if _, cands := r.CompleteToken(`import "str`); len(cands) == 0 {
		t.Fatal("open import literal lost its candidates")
	}
}

func TestCompleteRecursiveEmbed(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	// `type Node struct{ *Node }` is legal Go — the enumeration walk
	// must break the cycle, not recurse until the process dies.
	for _, line := range []string{
		`type Node struct { *Node; Value int }`,
		`var n Node`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	cands := r.Complete("n.")
	for _, want := range []string{"Node", "Value"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in n. -> %v", want, candNames(cands))
		}
	}
	// one level deeper still resolves instead of hanging
	cands = r.Complete("n.Node.")
	if !hasCand(cands, "Value") {
		t.Fatalf("Value missing in n.Node. -> %v", candNames(cands))
	}
}

func TestCompleteNilPointerFields(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	// `var p *Inner` holds a typed nil — no live pointee — but its
	// fields come from the declared element type.
	for _, line := range []string{
		`type Inner struct { X int }`,
		`var p *Inner`,
		`var pp **Inner`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	if !hasCand(r.Complete("p."), "X") {
		t.Fatalf("X missing in p. -> %v", candNames(r.Complete("p.")))
	}
	if !hasCand(r.Complete("pp."), "X") {
		t.Fatalf("X missing in pp. -> %v", candNames(r.Complete("pp.")))
	}
}

type hostInner struct{ ID int64 }
type hostOuter struct {
	hostInner
	Name string
}

func TestCompleteHostEmbeddedFields(t *testing.T) {
	e := NewEngine("testdata")
	r := e.NewREPL()
	r.pkg.Globals.Set("hv", &runtime.GoValue{V: hostOuter{Name: "x"}})

	// embedded fields promote: hv.ID is reachable through hostInner even
	// though NumField only counts the two direct fields.
	cands := r.Complete("hv.")
	for _, want := range []string{"Name", "ID"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in hv. -> %v", want, candNames(cands))
		}
	}
	if hasCand(cands, "hostInner") {
		t.Fatalf("unexported embedded field leaked: %v", candNames(cands))
	}
}

func TestCompleteHostType(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`import "strings"`,
		`var b strings.Builder`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	cands := r.Complete("b.Write")
	for _, want := range []string{"WriteString", "WriteByte", "WriteRune"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in b.Write -> %v", want, candNames(cands))
		}
	}
	// the type name also selects host methods
	if !hasCand(r.Complete("strings.Builder."), "WriteString") {
		t.Fatalf("WriteString missing in strings.Builder. -> %v", candNames(r.Complete("strings.Builder.")))
	}
}

func TestCompleteDirPackage(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	if _, err := r.EvalLine(ctx, `import "./inspectpkg"`); err != nil {
		t.Fatalf("EvalLine: %v", err)
	}
	cands := r.Complete("inspectpkg.")
	for _, want := range []string{"Hello", "User", "Count", "Label", "MyInt"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in inspectpkg. -> %v", want, candNames(cands))
		}
	}
	// unexported members stay hidden
	if hasCand(cands, "hiddenFn") {
		t.Fatalf("unexported member leaked: %v", candNames(cands))
	}
	// type-level members of a package type: `User.M` is a method
	// expression, so pointer receivers (Greet) stay out.
	user := r.Complete("inspectpkg.User.")
	if !hasCand(user, "Bye") {
		t.Fatalf("Bye missing in inspectpkg.User. -> %v", candNames(user))
	}
	if hasCand(user, "Greet") {
		t.Fatalf("ptr-receiver Greet leaked into inspectpkg.User. -> %v", candNames(user))
	}
	// enum members from the index; a var typed with the enum type is
	// not a member (T.Var is not valid Go), and untyped const specs
	// (FlagC/FlagD) break the type-inheritance chain.
	status := r.Complete("inspectpkg.Status.")
	for _, want := range []string{"StatusTodo", "StatusExtra", "FlagA", "FlagB"} {
		if !hasCand(status, want) {
			t.Fatalf("%s missing in inspectpkg.Status. -> %v", want, candNames(status))
		}
	}
	for _, not := range []string{"CurrentStatus", "FlagC", "FlagD"} {
		if hasCand(status, not) {
			t.Fatalf("%s leaked into inspectpkg.Status. -> %v", not, candNames(status))
		}
	}
	// unexported members of the uninitialised package stay hidden, and the
	// package never had to run init to answer.
}

func TestCompleteChainAndEmbeds(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`import "./inspectpkg"`,
		`var u inspectpkg.User`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	cands := r.Complete("u.")
	for _, want := range []string{"Name", "Age", "Base", "Builder", "Greet", "Bye"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in u. -> %v", want, candNames(cands))
		}
	}
	// a declared-typed field's members without a live value — Builder is
	// a NIL field slot answered through `Builder strings.Builder`.
	if !hasCand(r.Complete("u.Builder."), "WriteString") {
		t.Fatalf("WriteString missing in u.Builder. -> %v", candNames(r.Complete("u.Builder.")))
	}
	// promoted field through a nil embedded *Base — type-level fallback
	if !hasCand(r.Complete("u.I"), "ID") {
		t.Fatalf("ID missing in u.I -> %v", candNames(r.Complete("u.I")))
	}
	// selector inside a call fragment: the base walk finds the suffix
	if !hasCand(r.Complete("println(u."), "Greet") {
		t.Fatalf("Greet missing in println(u. -> %v", candNames(r.Complete("println(u.")))
	}
}

func TestCompleteDotImport(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	if _, err := r.EvalLine(ctx, `import . "./inspectpkg"`); err != nil {
		t.Fatalf("EvalLine: %v", err)
	}
	if !hasCand(r.Complete("Hel"), "Hello") {
		t.Fatalf("Hello missing in bare Hel -> %v", candNames(r.Complete("Hel")))
	}
}

func TestCompleteSelectors(t *testing.T) {
	ctx := context.Background()
	e := NewEngine("testdata")
	r := e.NewREPL()

	for _, line := range []string{
		`import "./inspectpkg"`,
		`xs := []inspectpkg.User{{Name: "x"}}`,
	} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("EvalLine(%q): %v", line, err)
		}
	}
	if !hasCand(r.Complete("xs[0]."), "Greet") {
		t.Fatalf("Greet missing in xs[0]. -> %v", candNames(r.Complete("xs[0].")))
	}
}

func TestCompleteImportPaths(t *testing.T) {
	e := NewEngine("testdata")
	r := e.NewREPL()

	// bound intrinsics + GOROOT stdlib share one namespace
	cands := r.Complete(`import "str`)
	for _, want := range []string{"strings", "strconv"} {
		if !hasCand(cands, want) {
			t.Fatalf("%s missing in import \"str -> %v", want, candNames(cands))
		}
	}
	if hasCand(cands, "strlen") {
		t.Fatalf("nonexistent path leaked: %v", candNames(cands))
	}

	// the module's own packages and its go.mod requires
	if !hasCand(r.Complete(`import "github.com/podhmo/minigo/`), "github.com/podhmo/minigo/resolve") {
		t.Fatalf("module package missing: %v", candNames(r.Complete(`import "github.com/podhmo/minigo/`)))
	}
	if !hasCand(r.Complete(`import "github.com/google/`), "github.com/google/go-cmp") {
		t.Fatalf("require missing: %v", candNames(r.Complete(`import "github.com/google/`)))
	}

	// the REPL's own `./` dir imports, relative to the engine cwd
	dc := r.Complete(`import "./`)
	if !hasCand(dc, "./inspectpkg") {
		t.Fatalf("./inspectpkg missing: %v", candNames(dc))
	}
	for _, cand := range dc {
		if !strings.HasPrefix(cand.Name, "./") {
			t.Fatalf("non-dir candidate leaked: %+v", cand)
		}
	}

	// alias and dot forms end in the same string context
	if !hasCand(r.Complete(`import s "str`), "strings") {
		t.Fatalf("strings missing under alias import: %v", candNames(r.Complete(`import s "str`)))
	}
	if !hasCand(r.Complete(`import . "str`), "strings") {
		t.Fatalf("strings missing under dot import: %v", candNames(r.Complete(`import . "str`)))
	}
}
