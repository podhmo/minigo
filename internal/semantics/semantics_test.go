package semantics

import (
	"errors"
	"go/ast"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/syntax"
)

// fakeSource is a Source backed by in-memory indexes; loads records
// which paths were asked so tests can assert initialization stayed
// untouched.
type fakeSource struct {
	indexes  map[string]*index.Index // already indexed
	loadable map[string]*index.Index // loadable but not yet indexed
	loadErr  map[string]error
	loaded   []string
}

func (f *fakeSource) Indexed(path string) *index.Index { return f.indexes[path] }

func (f *fakeSource) Load(path string) (*index.Index, error) {
	f.loaded = append(f.loaded, path)
	if err := f.loadErr[path]; err != nil {
		return nil, err
	}
	return f.loadable[path], nil
}

func indexOf(t *testing.T, src string) *index.Index {
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
	return ix
}

func TestTypeDeclOf(t *testing.T) {
	ix := indexOf(t, "package foo\ntype T struct{ X int }\nvar V int\n")

	t.Run("Found: declared type returns its type expr", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ctx := DeclContext{Pkg: "foo"}
		got := s.TypeDeclOf(ctx, "T", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind: %v", got.Kind)
		}
		st, ok := got.Val.Expr.(*ast.StructType)
		if !ok {
			t.Fatalf("expr is %T, not *ast.StructType", got.Val.Expr)
		}
		if diff := cmp.Diff(1, len(st.Fields.List)); diff != "" {
			t.Fatal(diff)
		}
		if diff := cmp.Diff(ctx.Pkg, got.Val.Ctx.Pkg); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("Missing: name declares no type", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		for _, name := range []string{"V", "Nope"} {
			got := s.TypeDeclOf(DeclContext{Pkg: "foo"}, name, IndexedOnly)
			if got.Kind != Missing {
				t.Fatalf("%s: kind %v, want Missing", name, got.Kind)
			}
		}
	})

	t.Run("Deferred: unindexed package under IndexedOnly", func(t *testing.T) {
		src := &fakeSource{indexes: map[string]*index.Index{}}
		s := New(src)
		got := s.TypeDeclOf(DeclContext{Pkg: "bar"}, "T", IndexedOnly)
		if got.Kind != Deferred {
			t.Fatalf("kind %v, want Deferred", got.Kind)
		}
		if len(src.loaded) != 0 {
			t.Fatalf("IndexedOnly loaded %v", src.loaded)
		}
	})

	t.Run("Found via AllowIndex: load indexes the package", func(t *testing.T) {
		src := &fakeSource{indexes: map[string]*index.Index{}, loadable: map[string]*index.Index{"foo": ix}}
		s := New(src)
		got := s.TypeDeclOf(DeclContext{Pkg: "foo"}, "T", AllowIndex)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]string{"foo"}, src.loaded); diff != "" {
			t.Fatalf("loaded: %s", diff)
		}
	})

	t.Run("Failed: loader error under AllowIndex", func(t *testing.T) {
		want := errors.New("no such package")
		src := &fakeSource{indexes: map[string]*index.Index{}, loadErr: map[string]error{"bar": want}}
		s := New(src)
		got := s.TypeDeclOf(DeclContext{Pkg: "bar"}, "T", AllowIndex)
		if got.Kind != Failed {
			t.Fatalf("kind %v, want Failed", got.Kind)
		}
		if !errors.Is(got.Err, want) {
			t.Fatalf("err %v, want %v", got.Err, want)
		}
	})
}
