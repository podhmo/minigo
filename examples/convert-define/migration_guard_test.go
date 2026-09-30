package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoMinigoV1Dependency is the dependency-guard half of the "convert-define
// on minigo" acceptance: after the migration no Go source in this module may
// import the v1 interpreter (github.com/podhmo/go-scan/minigo and any
// subpackage), and at least one file must import the rebooted interpreter
// (github.com/podhmo/minigo) — otherwise the check would also pass on a tree
// that uses neither.
func TestNoMinigoV1Dependency(t *testing.T) {
	const v1Prefix = "github.com/podhmo/go-scan/minigo"
	const interpImport = "github.com/podhmo/minigo"

	fset := token.NewFileSet()
	usesInterp := false
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if p == v1Prefix || strings.HasPrefix(p, v1Prefix+"/") {
				t.Errorf("%s: imports v1 minigo %q", path, p)
			}
			if p == interpImport || strings.HasPrefix(p, interpImport+"/") {
				usesInterp = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}
	if !usesInterp {
		t.Error("no file imports github.com/podhmo/minigo — the interpreter is absent")
	}
}
