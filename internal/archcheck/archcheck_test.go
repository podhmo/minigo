package archcheck_test

import (
	"os"
	"path/filepath"
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
