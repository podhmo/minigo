package source

import (
	"errors"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
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
			return &runtime.Package{PackageImage: runtime.PackageImage{Index: ix}}, nil
		}}
		v := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		if got := v.Indexed("foo"); got != nil {
			t.Fatalf("Indexed() = %v before materialize", got)
		}
		if diff := cmp.Diff(0, loads); diff != "" {
			t.Fatalf("Indexed triggered loads: %s", diff)
		}
	})

	t.Run("Indexed answers once materialized", func(t *testing.T) {
		p := &runtime.Package{PackageImage: runtime.PackageImage{Index: ix}}
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			return p, nil
		}}
		v := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		if _, err := ref.Materialize(); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(ix, v.Indexed("foo")); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("Load materializes to indexed, no init", func(t *testing.T) {
		loads := 0
		p := &runtime.Package{PackageImage: runtime.PackageImage{Index: ix}}
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			loads++
			return p, nil
		}}
		v := New(func(string) (*runtime.ImportRef, bool) { return ref, true })
		got, err := v.Load("foo")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(ix, got); diff != "" {
			t.Fatal(diff)
		}
		if diff := cmp.Diff(1, loads); diff != "" {
			t.Fatalf("loads: %s", diff)
		}
		if diff := cmp.Diff(false, p.State() >= runtime.Initializing); diff != "" {
			t.Fatalf("package initialized: %s", diff)
		}
	})

	t.Run("unknown path and loader error", func(t *testing.T) {
		v := New(func(string) (*runtime.ImportRef, bool) { return nil, false })
		if diff := cmp.Diff((*index.Index)(nil), v.Indexed("nope")); diff != "" {
			t.Fatal(diff)
		}
		got, err := v.Load("nope")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff((*index.Index)(nil), got); diff != "" {
			t.Fatal(diff)
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
		p := &runtime.Package{PackageImage: runtime.PackageImage{Index: ix}}
		ref := &runtime.ImportRef{Path: "foo", Load: func(string) (*runtime.Package, error) {
			return p, nil
		}}
		svc := semantics.New(New(func(string) (*runtime.ImportRef, bool) { return ref, true }))
		got := svc.TypeDeclOf(semantics.DeclContext{Pkg: "foo"}, "T", semantics.AllowIndex)
		if diff := cmp.Diff(semantics.Found, got.Kind); diff != "" {
			t.Fatal(diff)
		}
	})
}
