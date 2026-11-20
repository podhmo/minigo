package modulewalk

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/pkg/locator"
)

// fixture lays out one module (example.com/m) with packages a and b,
// plus a nested module (example.com/nested) holding package c.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                 "module example.com/m\n\ngo 1.25\n",
		"a/a.go":                 "package a\n\nimport (\n\t\"fmt\"\n\t\"example.com/m/b\"\n)\n\nvar _ = fmt.Sprint\nvar _ = b.B\n",
		"a/a_test.go":            "package a\n\nimport (\n\t\"testing\"\n\t\"example.com/m/b\"\n)\n\nfunc TestA(t *testing.T) { _ = b.B }\n",
		"b/b.go":                 "package b\n\nimport \"strings\"\n\nvar B = strings.Repeat\n",
		"nested/go.mod":          "module example.com/nested\n\ngo 1.25\n",
		"nested/c/c.go":          "package c\n\nimport \"example.com/m/b\"\n\nvar _ = b.B\n",
		"nested/c/zzz_x_test.go": "package c\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestImports(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	pi, err := w.Imports(context.Background(), "example.com/m/a")
	if err != nil {
		t.Fatal(err)
	}
	if pi.Name != "a" {
		t.Errorf("Name = %q", pi.Name)
	}
	if diff := cmp.Diff([]string{"example.com/m/b", "fmt"}, pi.Imports); diff != "" {
		t.Errorf("Imports (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"example.com/m/b", "testing"}, pi.TestImports); diff != "" {
		t.Errorf("TestImports (-want +got):\n%s", diff)
	}
	if len(pi.FileImports) != 2 {
		t.Errorf("FileImports covers %d files", len(pi.FileImports))
	}
	if pi.ModulePath != "example.com/m" {
		t.Errorf("ModulePath = %q", pi.ModulePath)
	}
}

func TestWalk(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	var seen []string
	err = w.Walk(context.Background(), VisitorFunc(func(pi *PackageImports) ([]string, error) {
		seen = append(seen, pi.ImportPath)
		var follow []string
		for _, imp := range pi.Imports {
			if imp == "example.com/m/b" { // only in-module imports can resolve
				follow = append(follow, imp)
			}
		}
		return follow, nil
	}), "example.com/m/a")
	if err != nil {
		t.Fatal(err)
	}
	// BFS: a first, then b once (visited set dedupes)
	if diff := cmp.Diff([]string{"example.com/m/a", "example.com/m/b"}, seen); diff != "" {
		t.Errorf("walk (-want +got):\n%s", diff)
	}
}

func TestWalkPattern(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	var seen []string
	err = w.Walk(context.Background(), VisitorFunc(func(pi *PackageImports) ([]string, error) {
		seen = append(seen, pi.ImportPath)
		return nil, nil // follow nothing: just enumerate
	}), "./...")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(seen)
	if diff := cmp.Diff([]string{"example.com/m/a", "example.com/m/b"}, seen); diff != "" {
		t.Errorf("walk (-want +got):\n%s", diff)
	}
}

func TestWalkRepo_NestedModule(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	var paths, modules []string
	err = w.WalkRepo(context.Background(), root, VisitorFunc(func(pi *PackageImports) ([]string, error) {
		paths = append(paths, pi.ImportPath)
		modules = append(modules, pi.ModulePath)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"example.com/m/a", "example.com/m/b", "example.com/nested/c"}, paths); diff != "" {
		t.Errorf("imports (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"example.com/m", "example.com/m", "example.com/nested"}, modules); diff != "" {
		t.Errorf("modules (-want +got):\n%s", diff)
	}
}

func TestFindImporters(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	importers, err := w.FindImporters(context.Background(), "example.com/m/b")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, pi := range importers {
		paths = append(paths, pi.ImportPath)
	}
	sort.Strings(paths)
	if diff := cmp.Diff([]string{"example.com/m/a", "example.com/nested/c"}, paths); diff != "" {
		t.Errorf("importers (-want +got):\n%s", diff)
	}
}

func TestReverseDependencyMap(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	deps, err := w.ReverseDependencyMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"example.com/m/b": {"example.com/m/a", "example.com/nested/c"},
		"fmt":             {"example.com/m/a"},
		"strings":         {"example.com/m/b"},
	}
	if diff := cmp.Diff(want, deps); diff != "" {
		t.Errorf("deps (-want +got):\n%s", diff)
	}
}

func TestUnscannedGoFiles(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc, IncludeTests: true}

	// before any scan, everything is unscanned
	got, err := w.UnscannedGoFiles("example.com/m/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("unscanned = %v", got)
	}
	// after scanning a, only the drift (none) remains
	if _, err := w.Imports(context.Background(), "example.com/m/a"); err != nil {
		t.Fatal(err)
	}
	got, err = w.UnscannedGoFiles("example.com/m/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unscanned after scan = %v", got)
	}
	// b was never scanned
	got, err = w.UnscannedGoFiles("example.com/m/b")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("unscanned b = %v", got)
	}
}

func TestDiscoverModules(t *testing.T) {
	root := fixture(t)

	dirs, err := DiscoverModules(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "nested"), root}
	sort.Strings(want)
	if diff := cmp.Diff(want, dirs); diff != "" {
		t.Errorf("modules (-want +got):\n%s", diff)
	}

	// go.work wins over the go.mod scan and may name sibling dirs
	ws := t.TempDir()
	m1 := filepath.Join(ws, "m1")
	m2 := filepath.Join(ws, "m2")
	for _, d := range []string{m1, m2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "go.mod"), []byte("module x\n\ngo 1.25\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "go.work"), []byte("go 1.25\n\nuse (\n\t./m1\n\t./m2\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err = DiscoverModules(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{m1, m2}, dirs); diff != "" {
		t.Errorf("go.work modules (-want +got):\n%s", diff)
	}
}

func TestWalkPattern_ExcludesVendorTestdata(t *testing.T) {
	root := fixture(t)
	for name, content := range map[string]string{
		"vendor/example.com/v/v.go": "package v\n",
		"testdata/golden/x.go":      "package golden\n",
		"_hidden/h.go":              "package hidden\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}
	w := &Walker{Locator: loc}

	var seen []string
	err = w.Walk(context.Background(), VisitorFunc(func(pi *PackageImports) ([]string, error) {
		seen = append(seen, pi.ImportPath)
		return nil, nil
	}), "./...")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(seen)
	if diff := cmp.Diff([]string{"example.com/m/a", "example.com/m/b"}, seen); diff != "" {
		t.Errorf("walk (-want +got):\n%s", diff)
	}
}

func TestFindImporters_IncludeTests(t *testing.T) {
	root := fixture(t)
	loc, err := locator.New(root)
	if err != nil {
		t.Fatal(err)
	}

	// "testing" is a test-file-only import of a: invisible until IncludeTests
	w := &Walker{Locator: loc}
	importers, err := w.FindImporters(context.Background(), "testing")
	if err != nil {
		t.Fatal(err)
	}
	if len(importers) != 0 {
		t.Errorf("importers without IncludeTests = %v", importers)
	}

	w = &Walker{Locator: loc, IncludeTests: true}
	importers, err = w.FindImporters(context.Background(), "testing")
	if err != nil {
		t.Fatal(err)
	}
	if len(importers) != 1 || importers[0].ImportPath != "example.com/m/a" {
		t.Errorf("importers with IncludeTests = %v", importers)
	}
}
