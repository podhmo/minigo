package source

// M5-3a: the source-version semantics of the shared caches — each axis
// that makes a "version" (file content, file set, language version)
// must swap the cached object for a fresh one, while a version already
// handed to a session keeps working unchanged.

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/syntax"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseCacheContentVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.go", "package a\n\nfunc One() {}\n")

	fset := token.NewFileSet()
	pc := NewParseCache()

	files1, _, err := pc.Files(fset, []string{path}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	files2, _, err := pc.Files(fset, []string{path}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	if files1[0] != files2[0] {
		t.Fatal("unchanged file must share the cached *syntax.File")
	}

	writeFile(t, dir, "a.go", "package a\n\nfunc Two() {}\n")
	files3, _, err := pc.Files(fset, []string{path}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	if files3[0] == files1[0] {
		t.Fatal("an edited file must re-parse to a new *syntax.File")
	}

	// the version a session already holds is never mutated in place:
	// files1[0] still declares One, files3[0] declares Two.
	names := func(sf *syntax.File) []string {
		var got []string
		for _, d := range sf.AST.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				got = append(got, fd.Name.Name)
			}
		}
		return got
	}
	if diff := cmp.Diff([]string{"One"}, names(files1[0])); diff != "" {
		t.Errorf("old version changed (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"Two"}, names(files3[0])); diff != "" {
		t.Errorf("new version wrong (-want +got):\n%s", diff)
	}
}

func TestParseCacheLangVersion(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.go", "package a\n\nfunc F() {}\n")

	fset := token.NewFileSet()
	pc := NewParseCache()

	files1, _, err := pc.Files(fset, []string{path}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	// same bytes, different language version — the stored file's fixed
	// LangMod is stale, so the load re-parses and re-stamps
	files2, _, err := pc.Files(fset, []string{path}, "go1.21")
	if err != nil {
		t.Fatal(err)
	}
	if files2[0] == files1[0] {
		t.Fatal("a changed language version must re-parse to a new *syntax.File")
	}
	if files1[0].LangMod != "go1.24" {
		t.Errorf("old version's LangMod mutated: %q", files1[0].LangMod)
	}
	if files2[0].LangMod != "go1.21" {
		t.Errorf("new version's LangMod = %q, want go1.21", files2[0].LangMod)
	}

	files3, _, err := pc.Files(fset, []string{path}, "go1.21")
	if err != nil {
		t.Fatal(err)
	}
	if files3[0] != files2[0] {
		t.Fatal("same content and language must share the cached *syntax.File")
	}
}

func TestIndexCacheFileSetVersion(t *testing.T) {
	dir := t.TempDir()
	pa := writeFile(t, dir, "a.go", "package a\n\nfunc A() {}\n")
	pb := writeFile(t, dir, "b.go", "package a\n\nfunc B() {}\n")

	fset := token.NewFileSet()
	pc := NewParseCache()
	ic := NewIndexCache()

	files, _, err := pc.Files(fset, []string{pa, pb}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	ix1, err := ic.IndexFor(files, false)
	if err != nil {
		t.Fatal(err)
	}
	ix2, err := ic.IndexFor(files, false)
	if err != nil {
		t.Fatal(err)
	}
	if ix1 != ix2 {
		t.Fatal("the same file set must reuse the one built index")
	}

	// the file SET is part of the key: an extra file is a new version
	extra, _, err := pc.Files(fset, []string{pa, pb, writeFile(t, dir, "c.go", "package a\n\nfunc C() {}\n")}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	ix3, err := ic.IndexFor(extra, false)
	if err != nil {
		t.Fatal(err)
	}
	if ix3 == ix1 {
		t.Fatal("a different file set must build a new index")
	}

	// order is part of the fingerprint too
	reordered := []*syntax.File{files[1], files[0]}
	ix4, err := ic.IndexFor(reordered, false)
	if err != nil {
		t.Fatal(err)
	}
	if ix4 == ix1 {
		t.Fatal("a reordered file set must build a new index")
	}

	// and a re-parse (edit) mints a new *syntax.File, so the same
	// list contents at new addresses are again a new version
	writeFile(t, dir, "a.go", "package a\n\nfunc A2() {}\n")
	files5, _, err := pc.Files(fset, []string{pa, pb}, "go1.24")
	if err != nil {
		t.Fatal(err)
	}
	ix5, err := ic.IndexFor(files5, false)
	if err != nil {
		t.Fatal(err)
	}
	if ix5 == ix1 {
		t.Fatal("an edited file must produce a new index version")
	}

	// the index a session already holds is untouched by the rebuild
	if ix1.Funcs["A"] == nil {
		t.Error("old index version lost its decls")
	}
	if ix5.Funcs["A2"] == nil {
		t.Error("new index version lacks the edited decl")
	}
}
