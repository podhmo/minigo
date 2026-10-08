package minigo

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestParsePackageFilesBases checks that the concurrent parse assigns
// the bases, line tables and node positions a sequential parse does.
func TestParsePackageFilesBases(t *testing.T) {
	paths, err := filepath.Glob("syntax/*.go")
	if err != nil || len(paths) < 2 {
		t.Fatalf("glob: %v (%d files)", err, len(paths))
	}
	type fileShape struct {
		Name      string
		Base      int
		Size      int
		Lines     []int
		Positions []string
	}
	shape := func(fset *token.FileSet, f *ast.File) fileShape {
		tf := fset.File(f.FileStart)
		var ps []string
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				ps = append(ps, fset.Position(id.Pos()).String())
			}
			return true
		})
		return fileShape{Name: tf.Name(), Base: tf.Base(), Size: tf.Size(), Lines: tf.Lines(), Positions: ps}
	}
	run := func(parse func(*token.FileSet, []string) ([]fileShape, error)) []fileShape {
		fset := token.NewFileSet()
		fset.AddFile("earlier.go", -1, 123) // a non-trivial starting base
		got, err := parse(fset, paths)
		if err != nil {
			t.Fatal(err)
		}
		// a later file lands after the parsed range, as sequentially
		got = append(got, fileShape{Name: "later.go", Base: fset.AddFile("later.go", -1, 7).Base()})
		return got
	}
	seq := run(func(fset *token.FileSet, ps []string) ([]fileShape, error) {
		files, _, err := parseFilesSeq(fset, ps)
		var out []fileShape
		for _, f := range files {
			out = append(out, shape(fset, f.AST))
		}
		return out, err
	})
	par := run(func(fset *token.FileSet, ps []string) ([]fileShape, error) {
		files, _, err := parsePackageFiles(fset, ps)
		var out []fileShape
		for _, f := range files {
			out = append(out, shape(fset, f.AST))
		}
		return out, err
	})
	if diff := cmp.Diff(seq, par); diff != "" {
		t.Errorf("concurrent parse differs from sequential (-seq +par):\n%s", diff)
	}
}

// TestParsePackageFilesError reports the first failing file in order.
func TestParsePackageFilesError(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.go": "package p\n", "b.go": "package p\nfunc (\n", "c.go": "package p\nvar = \n"})
	good, bad1, bad2 := filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go"), filepath.Join(dir, "c.go")
	_, failed, err := parsePackageFiles(token.NewFileSet(), []string{good, bad1, bad2})
	if err == nil {
		t.Fatal("no error")
	}
	if diff := cmp.Diff(bad1, failed); diff != "" {
		t.Errorf("failed file (-want +got):\n%s", diff)
	}
}
