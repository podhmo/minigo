package source

import (
	"go/ast"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// fieldPathsTd parses src, indexes it, and returns the typedef of
// `name`'s declaration — the shape e.fieldPath hands the adapter.
func fieldPathsTd(t *testing.T, pkg, src, name string) (*runtime.TypeDef, *index.Index) {
	t.Helper()
	fset := token.NewFileSet()
	sf, err := syntax.ParseFile(fset, "pkg.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Build([]*syntax.File{sf})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range sf.AST.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gd.Specs {
			if ts, ok := sp.(*ast.TypeSpec); ok && ts.Name.Name == name {
				return &runtime.TypeDef{
					Kind: runtime.KindStruct,
					Pkg:  &runtime.Package{Path: pkg},
					File: sf, Spec: ts,
				}, ix
			}
		}
	}
	t.Fatalf("no type %s in source", name)
	return nil, nil
}

// OfKeyed memoizes decided answers by (key, name): a selector asking
// twice — even through a different typedef descriptor with the same
// identity — must not re-explore the declaration graph (M4-1d).
func TestFieldPathsOfKeyed(t *testing.T) {
	src := `package foo
type E struct{ X, Y int }
type S struct{ E; Z int }
`
	td, ix := fieldPathsTd(t, "foo", src, "S")
	td2, _ := fieldPathsTd(t, "foo", src, "S") // a twin descriptor
	td2.File = td.File                         // same parse = same identity

	explorations := 0
	fps := NewFieldPaths(&IndexSource{
		IndexedFunc: func(path string) *index.Index { explorations++; return ix },
		LoadFunc:    func(path string) (*index.Index, error) { explorations++; return ix, nil },
	})

	first := fps.OfKeyed(td, "X", "k1")
	if first.Kind != FieldPathFound {
		t.Fatalf("first OfKeyed kind %v", first.Kind)
	}
	if diff := cmp.Diff([]int{0, 0}, first.Index); diff != "" {
		t.Fatal(diff)
	}
	afterFirst := explorations
	if afterFirst == 0 {
		t.Fatal("the first query never consulted the index")
	}

	// same key through a different descriptor: the memo answers, the
	// index is not re-explored.
	second := fps.OfKeyed(td2, "X", "k1")
	if diff := cmp.Diff(explorations, afterFirst); diff != "" {
		t.Fatalf("cached answer re-explored the index: %s", diff)
	}
	if second.Kind != FieldPathFound || !cmp.Equal(first.Index, second.Index) {
		t.Fatalf("cached answer %+v differs from %+v", second, first)
	}

	// a fresh key — a re-parse is a new source version — explores
	// again, so a stale path can never leak through.
	td3, _ := fieldPathsTd(t, "foo", src, "S")
	third := fps.OfKeyed(td3, "X", "k2")
	if diff := cmp.Diff(true, explorations > afterFirst); diff != "" {
		t.Fatalf("new version did not re-explore: %s", diff)
	}
	if diff := cmp.Diff(first.Index, third.Index); diff != "" {
		t.Fatal(diff)
	}

	// a different name under the same key gets its own entry.
	fourth := fps.OfKeyed(td, "Z", "k1")
	if fourth.Kind != FieldPathFound {
		t.Fatalf("Z kind %v", fourth.Kind)
	}
	if diff := cmp.Diff([]int{1}, fourth.Index); diff != "" {
		t.Fatal(diff)
	}

	// an empty key is uncached — used for typedefs without identity.
	before := explorations
	fps.Of(td, "X")
	fps.Of(td, "X")
	if diff := cmp.Diff(true, explorations > before+1); diff != "" {
		t.Fatalf("uncached queries did not re-explore: %s", diff)
	}
}
