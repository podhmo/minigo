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
