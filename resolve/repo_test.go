package resolve_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/resolve"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// ScanRepo claims every directory holding .go files for its nearest
// enclosing go.mod — a nested module reports its own module identity —
// and splits the file inventory at the *_test.go boundary. Directories
// the go tool ignores (vendor, testdata, .-/_-prefixed) never appear.
func TestScanRepo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/root\n\ngo 1.24\n")
	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, root, "a/a.go", "package a\n")
	writeFile(t, root, "a/a_test.go", "package a\n")
	writeFile(t, root, "a/b/b.go", "package b\n")
	writeFile(t, root, "onlytest/x_test.go", "package onlytest\n")
	writeFile(t, root, "vendor/v/v.go", "package v\n")
	writeFile(t, root, "testdata/t/t.go", "package t\n")
	writeFile(t, root, ".hidden/h.go", "package h\n")
	writeFile(t, root, "_under/u.go", "package u\n")
	writeFile(t, root, "a/b/.dot.go", "package b\n") // dot file ignored
	writeFile(t, root, "a/b/_gen.go", "package b\n") // underscore file ignored
	writeFile(t, root, "nested/go.mod", "module example.com/nested\n\ngo 1.24\n")
	writeFile(t, root, "nested/n.go", "package nested\n")
	writeFile(t, root, "nested/sub/s.go", "package sub\n")
	writeFile(t, root, "nested/sub/s_test.go", "package sub\n")

	pkgs, err := resolve.ScanRepo(root)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	type want struct {
		ModulePath string
		HasTests   bool
		GoFiles    int
		TestFiles  int
	}
	got := map[string]want{}
	for _, p := range pkgs {
		if p.ModuleDir == "" {
			t.Errorf("%s: ModuleDir empty inside a module tree", p.ImportPath)
		}
		if p.Dir != filepath.Join(p.ModuleDir, filepath.FromSlash(p.ImportPath[len(p.ModulePath):])) {
			t.Errorf("%s: Dir %q not under ModuleDir %q", p.ImportPath, p.Dir, p.ModuleDir)
		}
		got[p.ImportPath] = want{p.ModulePath, p.HasTests, len(p.GoFiles), len(p.TestFiles)}
	}
	exp := map[string]want{
		"example.com/root":          {"example.com/root", false, 1, 0},
		"example.com/root/a":        {"example.com/root", true, 1, 1},
		"example.com/root/a/b":      {"example.com/root", false, 1, 0},
		"example.com/root/onlytest": {"example.com/root", true, 0, 1},
		"example.com/nested":        {"example.com/nested", false, 1, 0},
		"example.com/nested/sub":    {"example.com/nested", true, 1, 1},
	}
	if diff := cmp.Diff(exp, got); diff != "" {
		t.Errorf("packages mismatch (-want +got):\n%s", diff)
	}
}

// A directory tree with no go.mod still enumerates — every package
// reports a synthetic <dir> import path and no module identity, the
// same spelling LocateDir gives an unclaimed directory.
func TestScanRepoNoModule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "p/p.go", "package p\n")
	writeFile(t, root, "p/p_test.go", "package p\n")

	pkgs, err := resolve.ScanRepo(root)
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("want 1 package, got %d", len(pkgs))
	}
	p := pkgs[0]
	wantPath := "<dir>" + filepath.ToSlash(filepath.Join(root, "p"))
	if p.ImportPath != wantPath {
		t.Errorf("ImportPath = %q, want %q", p.ImportPath, wantPath)
	}
	if p.ModuleDir != "" || p.ModulePath != "" {
		t.Errorf("module-less package reports module identity: %+v", p)
	}
	if !p.HasTests {
		t.Error("HasTests = false, want true")
	}
}

// A root holding no .go files answers an empty slice — an empty repo
// is a valid result, not a miss.
func TestScanRepoEmpty(t *testing.T) {
	pkgs, err := resolve.ScanRepo(t.TempDir())
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("want no packages, got %d", len(pkgs))
	}
}

// A missing root and a go.mod without a module directive are errors —
// silently skipping the broken file would mis-claim its subtree under
// the parent module.
func TestScanRepoErrors(t *testing.T) {
	if _, err := resolve.ScanRepo(filepath.Join(t.TempDir(), "no-such")); err == nil {
		t.Error("missing root: want error, got nil")
	}
	root := t.TempDir()
	writeFile(t, root, "go.mod", "go 1.24\n")
	writeFile(t, root, "p.go", "package p\n")
	if _, err := resolve.ScanRepo(root); err == nil {
		t.Error("go.mod without module directive: want error, got nil")
	}
}
