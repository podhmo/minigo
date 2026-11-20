package repl

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
)

// fixture: module example.com/t with a → {fmt, b} (test-file-only
// "testing"), b → strings, c → b only through c_test.go.
func pkginfoFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":      "module example.com/t\n\ngo 1.25\n",
		"a/a.go":      "package a\n\nimport (\n\t\"fmt\"\n\t\"example.com/t/b\"\n)\n\nvar _ = fmt.Sprint\nvar _ = b.B\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"b/b.go":      "package b\n\nimport \"strings\"\n\nvar B = strings.Repeat\n",
		"c/c.go":      "package c\n",
		"c/c_test.go": "package c\n\nimport \"example.com/t/b\"\n\nvar _ = b.B\n",
	})
	return dir
}

func TestReplImports(t *testing.T) {
	ctx := context.Background()
	r := New(minigo.NewEngine(pkginfoFixture(t)))

	got, err := r.Imports(ctx, "example.com/t/a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a (example.com/t/a)",
		"example.com/t/b",
		"fmt",
		"testing (test)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf(":imports (-want +got):\n%s", diff)
	}
}

func TestReplImports_DirRef(t *testing.T) {
	ctx := context.Background()
	r := New(minigo.NewEngine(pkginfoFixture(t)))

	got, err := r.Imports(ctx, "./b")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b (example.com/t/b)", "strings"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf(":imports (-want +got):\n%s", diff)
	}
}

func TestReplImports_NoCurrent(t *testing.T) {
	ctx := context.Background()
	r := New(minigo.NewEngine(pkginfoFixture(t)))
	if _, err := r.Imports(ctx, ""); err == nil {
		t.Error("empty ref without an entered package must fail")
	}
}

func TestReplImporters(t *testing.T) {
	ctx := context.Background()
	r := New(minigo.NewEngine(pkginfoFixture(t)))

	got, err := r.Importers(ctx, "example.com/t/b")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a (example.com/t/a)",
		"c (example.com/t/c) (test)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf(":importers (-want +got):\n%s", diff)
	}
}

func TestNestedModuleDir(t *testing.T) {
	root := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("go.mod", "module example.com/parent\n")
	writeFile("nested/go.mod", "module other.com/nested\n")

	cases := []struct {
		dir  string
		want bool
	}{
		{filepath.Join(root, "pkg"), false},
		{filepath.Join(root, "nested"), true},
		{filepath.Join(root, "nested", "pkg"), true},
		{root, false},
		{t.TempDir(), false},
	}
	for _, c := range cases {
		if got := nestedModuleDir(root, c.dir); got != c.want {
			t.Errorf("nestedModuleDir(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}
