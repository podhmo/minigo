package source

import (
	"errors"
	"go/token"
	"testing"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

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

func TestView(t *testing.T) {
	ix := indexOf(t, "package foo\ntype T int\n")

	t.Run("Indexed does not trigger a load", func(t *testing.T) {
		loads := 0
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			loads++
			return &runtime.Package{Index: ix}, nil
		}}
		v := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		if got := v.Indexed("foo"); got != nil {
			t.Fatalf("Indexed() = %v before materialize", got)
		}
		if loads != 0 {
			t.Fatalf("Indexed triggered %d loads", loads)
		}
	})

	t.Run("Indexed answers once materialized", func(t *testing.T) {
		p := &runtime.Package{Index: ix}
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			return p, nil
		}}
		v := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		if _, err := ref.Materialize(); err != nil {
			t.Fatal(err)
		}
		if got := v.Indexed("foo"); got != ix {
			t.Fatalf("Indexed() = %v, want %v", got, ix)
		}
	})

	t.Run("Load materializes to indexed, no init", func(t *testing.T) {
		loads := 0
		p := &runtime.Package{Index: ix}
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			loads++
			return p, nil
		}}
		v := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		got, err := v.Load("foo")
		if err != nil {
			t.Fatal(err)
		}
		if got != ix {
			t.Fatalf("Load() = %v, want %v", got, ix)
		}
		if loads != 1 {
			t.Fatalf("loads = %d", loads)
		}
		if p.State() != runtime.Unseen && p.State() >= runtime.Initializing {
			t.Fatalf("package initialized: state %v", p.State())
		}
	})

	t.Run("unknown path and loader error", func(t *testing.T) {
		v := New(func(string) (*runtime.ImportRef, bool) { return nil, false })
		if got := v.Indexed("nope"); got != nil {
			t.Fatalf("Indexed() = %v", got)
		}
		if got, err := v.Load("nope"); got != nil || err != nil {
			t.Fatalf("Load() = %v, %v", got, err)
		}

		want := errors.New("boom")
		ref := &runtime.ImportRef{Path: "x", Load: func(string) (*runtime.Package, error) {
			return nil, want
		}}
		v2 := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		if _, err := v2.Load("x"); !errors.Is(err, want) {
			t.Fatalf("err %v, want %v", err, want)
		}
	})

	t.Run("serves semantics.Service", func(t *testing.T) {
		p := &runtime.Package{Index: ix}
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			return p, nil
		}}
		svc := semantics.New(New(func(string) (*runtime.ImportRef, bool) { return ref, true }))
		got := svc.TypeDeclOf(semantics.DeclContext{Pkg: "foo"}, "T", semantics.AllowIndex)
		if got.Kind != semantics.Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
	})
}
