package archcheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/internal/archcheck"
	"github.com/podhmo/minigo/pkg/gentest"
)

// The allow list must stay in sync with the real tree: Build on the repo
// root must produce exactly the graph Rules describes.
func TestRulesMatchRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	g, err := archcheck.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if vs := archcheck.Check(g, archcheck.Rules); len(vs) > 0 {
		for _, v := range vs {
			t.Error(v.Message)
		}
	}
}

// Positive: an edge the allow list permits produces no violations.
func TestCheckAllowed(t *testing.T) {
	g := archcheck.Graph{"a": {"b"}, "b": {}}
	rules := map[string][]string{"a": {"b"}, "b": {}}
	if diff := cmp.Diff([]archcheck.Violation(nil), archcheck.Check(g, rules)); diff != "" {
		t.Error(diff)
	}
}

// Negative: an import edge outside the allow list is a violation, and so
// is an allow-list entry the code no longer uses.
func TestCheckViolations(t *testing.T) {
	g := archcheck.Graph{"a": {"c"}, "b": {}, "c": {}}
	rules := map[string][]string{"a": {"b"}, "b": {}, "c": {}}
	vs := archcheck.Check(g, rules)
	var msgs []string
	for _, v := range vs {
		msgs = append(msgs, v.Message)
	}
	want := []string{
		"a imports c, which the arch allow list does not permit",
		"a no longer imports b; drop the stale allow-list entry",
	}
	if diff := cmp.Diff(want, msgs); diff != "" {
		t.Error(diff)
	}
}

// Build on a synthetic module sees module-internal imports, ignores
// third-party imports, and does not descend into nested modules.
func TestBuildSyntheticModule(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.26\n",
		"m.go":       "package m\nvar V = 0\n",
		"a/a.go":     "package a\nimport (\n\t\"fmt\"\n\tm \"example.com/m\"\n\t\"example.com/m/b\"\n\t\"example.com/else\"\n)\nvar _ = fmt.Sprint\nvar _ = m.V\nvar _ = b.X\nvar _ = else.Y\n",
		"b/b.go":     "package b\nvar X = 1\n",
		"sub/go.mod": "module example.com/sub\n\ngo 1.26\n",
		"sub/s.go":   "package sub\n",
	})
	g, err := archcheck.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := archcheck.Graph{
		".": {},
		"a": {".", "b"},
		"b": {},
	}
	if diff := cmp.Diff(want, g); diff != "" {
		t.Error(diff)
	}
	// sub/ has its own go.mod and must not appear.
	if _, ok := g["sub"]; ok {
		t.Error("nested module sub/ was walked")
	}
}

// FileBudgets must match the real tree exactly: no unbudgeted files, no
// stale entries (M1-4).
func TestFileBudgetsMatchRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	sizes, err := archcheck.Sizes(root)
	if err != nil {
		t.Fatal(err)
	}
	if vs := archcheck.CheckSizes(sizes, archcheck.FileBudgets); len(vs) > 0 {
		for _, v := range vs {
			t.Error(v.Message)
		}
	}
}

// BannedCalls on the real repo: compile/ must never call
// ImportRef.Materialize or Package.EnsureReady directly — package
// queries go through the internal/source adapter (M2-4) — and the
// adapter itself must not reach package initialization (M2-2).
func TestBannedCallsRealRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	vs, err := archcheck.CheckBannedCalls(root, archcheck.BannedCalls)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]archcheck.Violation(nil), vs); diff != "" {
		t.Error(diff)
	}
}

// A source-side call into package initialization fires: the check the
// CLI and this test share must catch `.EnsureReady` and friends.
func TestCheckBannedCallsSourceInit(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":   "module example.com/m\n\ngo 1.26\n",
		"src/s.go": "package src\n\ntype P struct{}\nfunc (P) EnsureReady() {}\nfunc (P) Load() {}\n\nvar p P\nvar _ = p.EnsureReady()\nvar _ = p.Load()\n",
	})
	vs, err := archcheck.CheckBannedCalls(dir, map[string][]string{"src": {"EnsureReady", "EnsureReadyRun"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Pkg != "src" {
		t.Fatalf("want 1 src violation, got %+v", vs)
	}
}

// CheckSharedFields on the real repo: sharedAssets must stay clean of
// runtime execution-state types (M1-3).
func TestSharedFieldsRealRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	vs, err := archcheck.CheckSharedFields(root, ".", "sharedAssets", archcheck.SharedStateTypes, archcheck.SharedFuncExempt)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]archcheck.Violation(nil), vs); diff != "" {
		t.Error(diff)
	}
}

// CheckSharedFields on the real repo: PackageImage (M5-1) must stay
// clean of runtime execution-state types — a runtime ImportRef or a
// cell inside the immutable definition is a boundary violation, since
// those objects cache per-Session resolutions.
func TestPackageImageSharedFields(t *testing.T) {
	root := filepath.Join("..", "..")
	vs, err := archcheck.CheckSharedFields(root, "runtime", "PackageImage", archcheck.SharedStateTypes, archcheck.SharedFuncExempt)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]archcheck.Violation(nil), vs); diff != "" {
		t.Error(diff)
	}
}

// CheckSharedFields: synthetic module — clean struct passes, a struct
// holding a forbidden type or a non-exempt func field fails.
func TestCheckSharedFields(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":           "module example.com/m\n\ngo 1.26\n",
		"assets/assets.go": "package assets\nimport \"example.com/m/rt\"\n\ntype Good struct {\n\tN    int\n\tCb   func() // exempt below\n\tCell *rt.Cell // forbidden\n}\n\ntype Clean struct {\n\tS string\n\tW *Wrap\n}\n\ntype Wrap struct {\n\tVals []int\n}\n",
		"rt/rt.go":         "package rt\n\ntype Cell struct{ V int }\n",
	})
	root := dir
	forbidden := map[string]bool{"example.com/m/rt.Cell": true}
	exempt := map[string]bool{"Good.Cb": true}

	// Positive: no forbidden types anywhere under Clean.
	vs, err := archcheck.CheckSharedFields(root, "assets", "Clean", forbidden, exempt)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]archcheck.Violation(nil), vs); diff != "" {
		t.Error(diff)
	}

	// Negative: Good.Cell points at a forbidden type; Good.Cb is exempt.
	vs, err = archcheck.CheckSharedFields(root, "assets", "Good", forbidden, exempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(vs), vs)
	}
	if vs[0].Dep != "example.com/m/rt.Cell" {
		t.Errorf("unexpected violation: %s", vs[0].Message)
	}

	// Without the exemption the func field must also fire.
	vs, err = archcheck.CheckSharedFields(root, "assets", "Good", forbidden, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2 violations, got %d: %+v", len(vs), vs)
	}
}

// Regression: the same import alias bound to different paths in
// different files must still resolve each field through its own file.
func TestSharedFieldsAliasCollision(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":      "module example.com/m\n\ngo 1.26\n",
		"assets/a.go": "package assets\nimport rt \"example.com/m/rt\"\n\ntype Good struct {\n\tC *rt.Cell\n}\n",
		"assets/b.go": "package assets\nimport rt \"example.com/m/safe\"\n\ntype Also struct {\n\tC *rt.Thing\n}\n",
		"rt/rt.go":    "package rt\n\ntype Cell struct{ V int }\n",
		"safe/s.go":   "package safe\n\ntype Thing struct{ V int }\n",
	})
	forbidden := map[string]bool{"example.com/m/rt.Cell": true}
	vs, err := archcheck.CheckSharedFields(dir, "assets", "Good", forbidden, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Dep != "example.com/m/rt.Cell" {
		t.Fatalf("want 1 rt.Cell violation, got %+v", vs)
	}
}

// Regression: forbidden types inside generic instantiation args,
// anonymous structs, parens and named chan types must be seen.
func TestSharedFieldsNestedShapes(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"assets/assets.go": `package assets
import rt "example.com/m/rt"

type Box[T any] struct{ v T }

type ViaGeneric struct{ B Box[*rt.Cell] }
type ViaAnon    struct{ S struct{ C *rt.Cell } }
type ViaParen   struct{ C (*rt.Cell) }
type ViaChan    struct{ C CellChan }

type CellChan chan *rt.Cell
`,
		"rt/rt.go": "package rt\n\ntype Cell struct{ V int }\n",
	})
	forbidden := map[string]bool{"example.com/m/rt.Cell": true}
	for _, strct := range []string{"ViaGeneric", "ViaAnon", "ViaParen", "ViaChan"} {
		vs, err := archcheck.CheckSharedFields(dir, "assets", strct, forbidden, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) != 1 {
			t.Errorf("%s: want 1 violation, got %+v", strct, vs)
		}
	}
}

// Regression: a parenthesized named struct decl must still match
// "Type.Field" exemptions — not the doubled "Type.Type.Field".

// Regression: a parenthesized named struct decl must still match
// "Type.Field" exemptions — not the doubled "Type.Type.Field".
func TestSharedFieldsParenExempt(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":           "module example.com/m\n\ngo 1.26\n",
		"assets/assets.go": "package assets\n\ntype Good (struct{ Cb func() })\n",
	})
	vs, err := archcheck.CheckSharedFields(dir, "assets", "Good", nil, map[string]bool{"Good.Cb": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("exempt Cb reported: %+v", vs)
	}
}

// Sizes skips _test.go files, generated files and nested modules;
// CheckSizes fails on growth, unlisted files, and stale budgets alike.
func TestSizesAndBudgets(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":     "module example.com/m\n\ngo 1.26\n",
		"a.go":       "package m\n\nvar A = 1\n",
		"a_test.go":  "package m\n\nvar B = 2\n",
		"gen.go":     "// Code generated by tool. DO NOT EDIT.\n\npackage m\n\nvar G = 3\n",
		"marker.go":  "package m\n\n// Marker mentions it, but this file is hand-written:\nconst Marker = \"Code generated by tool. DO NOT EDIT.\"\n",
		"sub/go.mod": "module example.com/sub\n\ngo 1.26\n",
		"sub/s.go":   "package sub\n",
	})
	sizes, err := archcheck.Sizes(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a.go": 4, "marker.go": 5}
	if diff := cmp.Diff(want, sizes); diff != "" {
		t.Error(diff)
	}

	// Exact budget passes.
	if vs := archcheck.CheckSizes(sizes, map[string]int{"a.go": 4, "marker.go": 5}); len(vs) != 0 {
		t.Fatalf("want no violations, got %+v", vs)
	}

	// Smaller budget than reality fails; new file fails; stale entry fails.
	vs := archcheck.CheckSizes(map[string]int{"a.go": 4, "new.go": 2},
		map[string]int{"a.go": 3, "gone.go": 10})
	if len(vs) != 3 {
		t.Fatalf("want 3 violations, got %d: %+v", len(vs), vs)
	}
}

// BannedSelectors flags the named selector only inside the given package
// dirs.
func TestBannedSelectors(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.26\n",
		"p/p.go": "package p\nfunc f(x T) { x.Materialize() }\ntype T interface{ Materialize() }\n",
		"q/q.go": "package q\nfunc f(x T) { x.Materialize() }\ntype T interface{ Materialize() }\n",
	})
	reps, err := archcheck.BannedSelectors(dir, []string{"p"}, map[string]bool{"Materialize": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || reps[0].Pkg != "p" || reps[0].Name != "Materialize" {
		t.Fatalf("want one report in p, got %+v", reps)
	}
	if _, err := os.Stat(reps[0].File); err != nil {
		t.Fatal(err)
	}
}

// BannedImports on the real repo: vm/ must never import reflect — host
// value work goes through internal/hostbridge (P2-7).
func TestBannedImportsRealRepo(t *testing.T) {
	root := filepath.Join("..", "..")
	if vs := archcheck.CheckBannedImports(root, archcheck.BannedImports); len(vs) > 0 {
		for _, v := range vs {
			t.Error(v.Message)
		}
	}
}

// Negative: a vm file importing reflect is a violation.
func TestCheckBannedImportsViolation(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":   "module example.com/m\n\ngo 1.26\n",
		"vm/v.go":  "package vm\nimport \"reflect\"\nvar _ = reflect.ValueOf\n",
		"vm/ok.go": "package vm\nvar _ = 0\n",
	})
	vs := archcheck.CheckBannedImports(dir, map[string][]string{"vm": {"reflect"}})
	var msgs []string
	for _, v := range vs {
		msgs = append(msgs, v.Message)
	}
	want := []string{"vm/v.go imports reflect, which is banned here (host-value work goes through internal/hostbridge)"}
	if diff := cmp.Diff(want, msgs); diff != "" {
		t.Error(diff)
	}
}

func TestCheckBudgetGrowth(t *testing.T) {
	base := map[string]int{"a.go": 100, "gone.go": 50}
	// shrink, same size, a new path and a removed path all pass.
	if vs := archcheck.CheckBudgetGrowth(base, map[string]int{"a.go": 90, "new.go": 10}, nil); len(vs) != 0 {
		t.Fatalf("unexpected violations: %v", vs)
	}
	// a raise on an existing path is rejected...
	cur := map[string]int{"a.go": 120}
	vs := archcheck.CheckBudgetGrowth(base, cur, nil)
	if len(vs) != 1 || !strings.Contains(vs[0].Message, "a.go") {
		t.Fatalf("expected one budget-growth violation for a.go, got %v", vs)
	}
	// ...unless the path is listed as an explicit exception.
	if vs := archcheck.CheckBudgetGrowth(base, cur, map[string]string{"a.go": "generated table regen"}); len(vs) != 0 {
		t.Fatalf("exception not honored: %v", vs)
	}
}

func TestParseFileBudgetsFile(t *testing.T) {
	dir := t.TempDir()
	src := `package archcheck

var FileBudgets = map[string]int{
	"a.go": 100,
	"dir/b.go": 42,
}

var FileBudgetExceptions = map[string]string{}
`
	path := filepath.Join(dir, "budgets.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := archcheck.ParseFileBudgetsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a.go": 100, "dir/b.go": 42}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("budgets mismatch (-want +got):\n%s", diff)
	}
}
