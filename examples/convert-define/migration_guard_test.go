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
// import github.com/podhmo/go-scan or any of its subpackages (the go-scan
// sources it needs are vendored under pkg/), and at least one file must
// import the rebooted interpreter (github.com/podhmo/minigo) — otherwise the
// check would also pass on a tree that uses neither.
func TestNoMinigoV1Dependency(t *testing.T) {
	const goScanModule = "github.com/podhmo/go-scan"
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
			if p == goScanModule || strings.HasPrefix(p, goScanModule+"/") {
				t.Errorf("%s: imports go-scan %q", path, p)
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
