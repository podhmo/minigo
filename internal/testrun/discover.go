package testrun

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// testItem is one discovered `func TestXxx(t *testing.T)`.
type testItem struct {
	Name string      // declared name: "TestXxx"
	Pos  string      // declaration position, "file:line"
	File string      // source file (slash-normalized absolute path)
	Decl *index.Decl // the index decl the runner materializes through Engine.Call
}

// unsupportedDecl is a declaration `go test` semantics cover but
// `minigo test` v1 does not: the runner reports each loudly, and a
// TestMain also prevents the suite from running at all.
type unsupportedDecl struct {
	Name  string // declared name: "BenchmarkXxx"
	Pos   string // "file:line"
	What  string // "benchmark", "fuzz target", "example", "TestMain"
	Fatal bool   // true only for TestMain — running tests it would have gated diverges
}

// malformed is a `Test`-prefixed function whose signature is not the
// test shape — go vet would flag it; the runner reports it as a
// warning but keeps running.
type malformed struct {
	Name string
	Pos  string
	Why  string
}

// discovery is the classified decl list of a built variant package.
type discovery struct {
	Tests       []testItem
	Unsupported []unsupportedDecl
	Malformed   []malformed
}

// discoverTests walks the package index (source order) and classifies
// the Test/Benchmark/Fuzz/Example/TestMain declarations found in the
// internal test files. Decls outside testFiles are ignored — a TestXxx
// in a non-test .go file is a regular function, as go test sees it.
func discoverTests(pkg *runtime.Package, testFiles map[string]bool, dir string) *discovery {
	out := &discovery{}
	for _, d := range pkg.Index.Decls {
		if d.Kind != index.FuncDecl || d.Func == nil || d.File == nil {
			continue
		}
		if !testFiles[d.File.Name] {
			continue
		}
		pos := declPos(pkg, d, dir)
		name := d.Name
		switch {
		case name == "TestMain":
			out.Unsupported = append(out.Unsupported, unsupportedDecl{
				Name: name, Pos: pos, What: "TestMain", Fatal: true,
			})
		case isTestName(name, "Test"):
			if isTestFunc(d.Func, d.File, "T") {
				out.Tests = append(out.Tests, testItem{Name: name, Pos: pos, File: d.File.Name, Decl: d})
			} else {
				out.Malformed = append(out.Malformed, malformed{Name: name, Pos: pos,
					Why: "want func TestXxx(t *testing.T)"})
			}
		case isTestName(name, "Benchmark"):
			if isTestFunc(d.Func, d.File, "B") {
				out.Unsupported = append(out.Unsupported, unsupportedDecl{
					Name: name, Pos: pos, What: "benchmark"})
			} else {
				out.Malformed = append(out.Malformed, malformed{Name: name, Pos: pos,
					Why: "want func BenchmarkXxx(b *testing.B)"})
			}
		case isTestName(name, "Fuzz"):
			if isTestFunc(d.Func, d.File, "F") {
				out.Unsupported = append(out.Unsupported, unsupportedDecl{
					Name: name, Pos: pos, What: "fuzz target"})
			} else {
				out.Malformed = append(out.Malformed, malformed{Name: name, Pos: pos,
					Why: "want func FuzzXxx(f *testing.F)"})
			}
		case isTestName(name, "Example"):
			// An example with an // Output: comment would actually run
			// under go test — skipping it silently diverges, so it is
			// fatal. Without Output: it never runs anyway (warning only).
			out.Unsupported = append(out.Unsupported, unsupportedDecl{
				Name: name, Pos: pos, What: "example", Fatal: hasOutputComment(d.Func)})
		}
	}
	return out
}

// declPos renders the declaration's position relative to the package
// dir when possible, like go test's "foo_test.go:12" spelling.
func declPos(pkg *runtime.Package, d *index.Decl, dir string) string {
	p := pkg.Fset.Position(d.Func.Pos())
	file := p.Filename
	if rel, err := filepath.Rel(dir, file); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		file = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", file, p.Line)
}

// isTestName reports whether name is a test-like declaration under
// prefix ("Test", "Benchmark", "Fuzz", "Example"): the byte after the
// prefix must not be a lowercase letter (so "Testx" is not a test) —
// the same rule cmd/go's testmain generation applies.
func isTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true // "Test" itself is a test name
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// isTestFunc reports whether fn has the signature `func(*testing.<member>)`:
// exactly one parameter whose type is `*pkg.Member` where pkg is this
// file's local name for the "testing" import path — including renamed
// (`import tt "testing"`) and dot imports.
func isTestFunc(fn *ast.FuncDecl, file *syntax.File, member string) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || fn.Type.Results != nil {
		return false
	}
	fld := fn.Type.Params.List[0]
	star, ok := fld.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := star.X.(type) {
	case *ast.SelectorExpr:
		// *pkg.Member
		sel, ok := x.X.(*ast.Ident)
		if !ok || x.Sel.Name != member {
			return false
		}
		return importPathOf(file, sel.Name) == "testing"
	case *ast.Ident:
		// *Member via `import . "testing"`
		if x.Name != member {
			return false
		}
		return importPathOf(file, ".") == "testing"
	}
	return false
}

// importPathOf maps a file's local import name to its path: the
// package-name basename for a plain import, an explicit alias, or "."
// for a dot import.
func importPathOf(file *syntax.File, local string) string {
	for _, imp := range file.Imports {
		if imp.LocalName() == local {
			return imp.Path
		}
	}
	return ""
}

// hasOutputComment reports whether the function's doc comment contains
// the "// Output:" marker that makes an Example runnable.
func hasOutputComment(fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	for _, c := range fn.Doc.List {
		if strings.Contains(c.Text, "Output:") {
			return true
		}
	}
	return false
}
