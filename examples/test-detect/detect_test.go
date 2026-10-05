package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// writeTree materializes a synthetic repository: path -> file content.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// testRepo builds a two-module tree:
//
//	example.com/m   a <- b <- c        (b's external _test pkg also imports a and tdep)
//	                a <- d <- e        (d has no tests)
//	                a <- depgen        ("generated" style package, has tests)
//	                tdep, util, broken (util: isolated; broken: unparseable)
//	example.com/sub x imports a        (cross-module edge)
func testRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":            "module example.com/m\n\ngo 1.26.0\n",
		"a/a.go":            "package a\n",
		"a/a_test.go":       "package a\n",
		"a/testdata/fix.go": "package fix\n", // skipped by the walk
		"b/b.go":            "package b\n\nimport \"example.com/m/a\"\n",
		"b/b_test.go":       "package b_test\n\nimport (\n\t\"example.com/m/a\"\n\t\"example.com/m/tdep\"\n)\n",
		"c/c.go":            "package c\n\nimport \"example.com/m/b\"\n",
		"c/c_test.go":       "package c\n",
		"d/d.go":            "package d\n\nimport \"example.com/m/a\"\n", // no tests
		"e/e.go":            "package e\n\nimport \"example.com/m/d\"\n",
		"e/e_test.go":       "package e\n",
		"depgen/depgen.go":  "package depgen\n\nimport \"example.com/m/a\"\n",
		"depgen/d_test.go":  "package depgen\n",
		"tdep/t.go":         "package tdep\n",
		"tdep/t_test.go":    "package tdep\n",
		"util/u.go":         "package util\n",                 // isolated, no tests
		"broken/b.go":       "package broken\nfunc broken(\n", // body error: invisible to ImportsOnly by design
		"sub/go.mod":        "module example.com/sub\n\ngo 1.26.0\n",
		"sub/x/x.go":        "package x\n\nimport \"example.com/m/a\"\n",
		"sub/x/x_test.go":   "package x\n",
	})
	return root
}

func keptPaths(t *testing.T, d *detection) []string {
	t.Helper()
	var paths []string
	for _, p := range d.packages {
		paths = append(paths, p.importPath)
	}
	return paths
}

func droppedPaths(t *testing.T, d *detection) []string {
	t.Helper()
	var paths []string
	for _, p := range d.dropped {
		paths = append(paths, p.importPath)
	}
	return paths
}

func TestDetectChangedPropagatesThroughReverseDeps(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, []string{"a/a.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"example.com/m/a",
		"example.com/m/b",
		"example.com/m/c",
		"example.com/m/depgen",
		"example.com/m/e",
		"example.com/sub/x",
	}
	if diff := cmp.Diff(want, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
	// d has no tests and is excluded from output, but the change still
	// reached e through it.
	if diff := cmp.Diff([]string{"example.com/m/d"}, droppedPaths(t, d)); diff != "" {
		t.Errorf("dropped mismatch (-want +got):\n%s", diff)
	}
	if len(d.warnings) != 0 {
		t.Errorf("unexpected warnings: %v", d.warnings)
	}
}

func TestTestOnlyDependencyCreatesAnEdge(t *testing.T) {
	root := testRepo(t)
	// tdep is imported only by b's external test package.
	d, err := detectChanged(root, []string{"tdep/t.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com/m/b", "example.com/m/c", "example.com/m/tdep"}
	if diff := cmp.Diff(want, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
}

func TestChangeToTestFileResolvesToItsPackage(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, []string{"c/c_test.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"example.com/m/c"}, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
}

func TestNestedModuleChangeStaysInModule(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, []string{"sub/x/x.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"example.com/sub/x"}, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
}

func TestIncludeUntested(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, []string{"a/a.go"}, options{includeUntested: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"example.com/m/a",
		"example.com/m/b",
		"example.com/m/c",
		"example.com/m/d",
		"example.com/m/depgen",
		"example.com/m/e",
		"example.com/sub/x",
	}
	if diff := cmp.Diff(want, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
}

func TestExcludeDropsMatchedPathsOnly(t *testing.T) {
	root := testRepo(t)
	re := regexp.MustCompile(`depgen`)
	d, err := detectChanged(root, []string{"a/a.go"}, options{exclude: []*regexp.Regexp{re}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"example.com/m/a",
		"example.com/m/b",
		"example.com/m/c",
		"example.com/m/e",
		"example.com/sub/x",
	}
	if diff := cmp.Diff(want, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"example.com/m/d", "example.com/m/depgen"}, droppedPaths(t, d)); diff != "" {
		t.Errorf("dropped mismatch (-want +got):\n%s", diff)
	}
}

func TestUnresolvableFilesWarnButDoNotFail(t *testing.T) {
	root := testRepo(t)
	changed := []string{
		"gone/g.go",         // deleted directory
		"a/testdata/fix.go", // testdata is not scanned
		"docs/README.md",    // non-go input: ignored silently
		"a/no-such-file.go", // missing file in a live dir: resolves via dir
		"README.md",         // exists at root but not .go
	}
	writeTree(t, root, map[string]string{"README.md": "hi\n"})
	d, err := detectChanged(root, changed, options{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{
		"example.com/m/a",
		"example.com/m/b",
		"example.com/m/c",
		"example.com/m/depgen",
		"example.com/m/e",
		"example.com/sub/x",
	}, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
	if len(d.warnings) != 2 {
		t.Fatalf("want 2 warnings, got %d: %v", len(d.warnings), d.warnings)
	}
}

func TestRenderDirAndJSON(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, []string{"a/a.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}

	out, err := d.render("dir")
	if err != nil {
		t.Fatal(err)
	}
	wantDirs := "a\nb\nc\ndepgen\ne\nsub/x\n"
	if diff := cmp.Diff(wantDirs, string(out)); diff != "" {
		t.Errorf("dir output mismatch (-want +got):\n%s", diff)
	}

	out, err = d.render("json")
	if err != nil {
		t.Fatal(err)
	}
	var groups []moduleGroup
	if err := json.Unmarshal(out, &groups); err != nil {
		t.Fatal(err)
	}
	want := []moduleGroup{
		{ModuleDir: ".", ModulePath: "example.com/m", Packages: []string{
			"example.com/m/a", "example.com/m/b", "example.com/m/c",
			"example.com/m/depgen", "example.com/m/e",
		}},
		{ModuleDir: "sub", ModulePath: "example.com/sub", Packages: []string{
			"example.com/sub/x",
		}},
	}
	if diff := cmp.Diff(want, groups); diff != "" {
		t.Errorf("json output mismatch (-want +got):\n%s", diff)
	}
}

func TestRenderSpace(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, []string{"sub/x/x.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.render("space")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "example.com/sub/x\n"; got != want {
		t.Errorf("space output = %q, want %q", got, want)
	}
}

func TestEmptyChangeSetProducesEmptyOutput(t *testing.T) {
	root := testRepo(t)
	d, err := detectChanged(root, nil, options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"pkg", "space", "dir"} {
		out, err := d.render(format)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Errorf("format %q: want empty output, got %q", format, out)
		}
	}
	// json is the exception: an empty set is a valid empty array so a
	// pipeline still parses the output, not the literal "null" a nil
	// slice would marshal to.
	out, err := d.render("json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "[]\n"; got != want {
		t.Errorf("format json: got %q, want %q", got, want)
	}
}

func TestModulePathOfHandlesCommentsAndQuotes(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		content string
		want    string
	}{
		{"module example.com/m\n", "example.com/m"},
		{"module example.com/m // trailing comment\n", "example.com/m"},
		{"module \"example.com/m\"\n", "example.com/m"},
		{"module \"example.com/m\" // both\n", "example.com/m"},
		{"// lead\nmodule example.com/m\ngo 1.26.0\n", "example.com/m"},
	} {
		path := filepath.Join(dir, "go.mod")
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := modulePathOf(path)
		if err != nil {
			t.Fatalf("content %q: %v", tc.content, err)
		}
		if got != tc.want {
			t.Errorf("content %q: got %q, want %q", tc.content, got, tc.want)
		}
	}
}

func TestDirectoryAndBackslashInputsWarn(t *testing.T) {
	root := testRepo(t)
	writeTree(t, root, map[string]string{"x.go/inner.go": "package z\n"})
	inputs := []string{
		"a",      // a real directory, no .go suffix: used to drop silently
		"x.go",   // a directory whose name ends in .go: used to mis-seed its parent
		`a\a.go`, // Windows-style separator on a unix run
		"a/a.go", // a real file still resolves
	}
	// A real file may legitimately contain '\' on unix: it must resolve,
	// not warn. Only nonexistent '\' paths are Windows-separator mistakes.
	unixBackslash := filepath.Separator != '\\'
	if unixBackslash {
		writeTree(t, root, map[string]string{`a/we\ird.go`: "package a\n"})
		inputs = append(inputs, `a/we\ird.go`)
	}
	d, err := detectChanged(root, inputs, options{})
	if err != nil {
		t.Fatal(err)
	}
	var warnText string
	for _, w := range d.warnings {
		warnText += w + "\n"
	}
	for _, want := range []string{"a: is a directory", "x.go: is a directory", `a\a.go: contains '\'`} {
		if !strings.Contains(warnText, want) {
			t.Errorf("want a warning containing %q, got:\n%s", want, warnText)
		}
	}
	if unixBackslash && strings.Contains(warnText, `we\ird.go`) {
		t.Errorf("existing a/we\\ird.go must not warn, got:\n%s", warnText)
	}
	// Only the real file seeded anything.
	if len(keptPaths(t, d)) == 0 {
		t.Error("a/a.go should still seed its affected set")
	}
}

func TestGoModNamedDirectoryDoesNotHidePackage(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":             "module example.com/m\n",
		"a/a.go":             "package a\n",
		"a/a_test.go":        "package a\n",
		"a/go.mod/keepme.md": "notes\n", // a subdirectory named go.mod is not a module
	})
	d, err := detectChanged(root, []string{"a/a.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"example.com/m/a"}, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
}

func TestParseErrorKeepsRecoveredImportsAndWarns(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":          "module example.com/m\n",
		"a/a.go":          "package a\n",
		"a/a_test.go":     "package a\n",
		"bad/bad.go":      "package bad\n\nimport \"example.com/m/a\"\nimport (\n", // truncated decl
		"bad/bad_test.go": "package bad\n",
	})
	d, err := detectChanged(root, []string{"a/a.go"}, options{})
	if err != nil {
		t.Fatal(err)
	}
	// The truncated second import decl errors, but the recovered import
	// still creates the a→bad edge — and the failure is loud.
	if diff := cmp.Diff([]string{"example.com/m/a", "example.com/m/bad"}, keptPaths(t, d)); diff != "" {
		t.Errorf("kept mismatch (-want +got):\n%s", diff)
	}
	found := false
	for _, w := range d.warnings {
		if strings.Contains(w, "bad.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a parse warning mentioning bad.go, got %v", d.warnings)
	}
}
