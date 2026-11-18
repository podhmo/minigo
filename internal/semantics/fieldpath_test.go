package semantics

import (
	"go/ast"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
)

// refFor returns the TypeRef the name's declaration in pkg yields —
// the TypeDeclOf answer a caller holds before asking for a path.
func refFor(t *testing.T, s *Service, pkg, name string) TypeRef {
	t.Helper()
	r := s.TypeDeclOf(DeclContext{Pkg: pkg}, name, AllowIndex)
	if r.Kind != Found {
		t.Fatalf("%s.%s: TypeDeclOf kind %v (err %v)", pkg, name, r.Kind, r.Err)
	}
	return r.Val
}

func TestFieldPathOf(t *testing.T) {
	foo := indexOf(t, `package foo
type E struct{ X, Y int }
type F struct{ X int }
type D struct{ F }
type A struct{ X int }
type B struct{ X int }
type I interface{ M() }
type Alias = E
type G[T any] struct{ X T; Y int }
type N int
type S struct {
	E
	X int
}
`)
	inner := indexOf(t, `package inner
type T struct{ X int; u int }
`)
	bar := indexOf(t, `package bar
import "x/inner"
type S struct{ inner.T }
`)

	t.Run("direct field", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo}})
		ref := refFor(t, s, "foo", "E")
		got := s.FieldPathOf(ref, "Y", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{1}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
		if !got.Val.Exported || got.Val.PkgPath != "foo" {
			t.Fatalf("val %+v", got.Val)
		}
	})

	t.Run("direct field wins over embedded", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		// S.X (index 1) is shallower than the promoted E.X ([0,0]).
		if diff := cmp.Diff([]int{1}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("promoted through embedding", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo}})
		ref := refFor(t, s, "foo", "D")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("embedded field is selectable by type name", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "E", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("ambiguous at the same depth", func(t *testing.T) {
		ix := indexOf(t, `package foo
type A struct{ X int }
type B struct{ X int }
type S struct{ A; B }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Failed {
			t.Fatalf("kind %v, want Failed", got.Kind)
		}
		if got.Err == nil {
			t.Fatal("ambiguous selection must carry a cause")
		}
	})

	t.Run("unexported fields still collide", func(t *testing.T) {
		ix := indexOf(t, `package foo
type A struct{ x int }
type B struct{ x int }
type S struct{ A; B }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "x", IndexedOnly)
		if got.Kind != Failed {
			t.Fatalf("kind %v, want Failed — unexported fields still compete at their depth", got.Kind)
		}
	})

	t.Run("unexported field reports its declaring package", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo, "x/inner": inner, "bar": bar}})
		ref := refFor(t, s, "bar", "S")
		got := s.FieldPathOf(ref, "u", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if got.Val.Exported || got.Val.PkgPath != "x/inner" {
			t.Fatalf("val %+v, want unexported field declared in x/inner", got.Val)
		}
	})

	t.Run("qualified embed resolves through the file's imports", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo, "x/inner": inner, "bar": bar}})
		ref := refFor(t, s, "bar", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("embedded pointer promotes", func(t *testing.T) {
		ix := indexOf(t, `package foo
type E struct{ X int }
type S struct{ *E }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("shallower promotion beats a deeper one", func(t *testing.T) {
		ix := indexOf(t, `package foo
type E struct{ X int }
type D struct{ E }
type S struct{ E; D }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("alias embedded type promotes its target", func(t *testing.T) {
		ix := indexOf(t, `package foo
type E struct{ X int }
type A = E
type S struct{ A }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("generic instantiation promotes", func(t *testing.T) {
		ix := indexOf(t, `package foo
type G[T any] struct{ X T; Y int }
type S struct{ G[int] }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "Y", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 1}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("interface embed promotes no fields", func(t *testing.T) {
		ix := indexOf(t, `package foo
type I interface{ M() }
type S struct{ I; X int }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "M", IndexedOnly)
		if got.Kind != Missing {
			t.Fatalf("kind %v, want Missing", got.Kind)
		}
	})

	t.Run("Missing: no such field and non-struct types", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo}})
		ref := refFor(t, s, "foo", "E")
		if got := s.FieldPathOf(ref, "Z", IndexedOnly); got.Kind != Missing {
			t.Fatalf("kind %v, want Missing", got.Kind)
		}
		nref := refFor(t, s, "foo", "N")
		if got := s.FieldPathOf(nref, "X", IndexedOnly); got.Kind != Missing {
			t.Fatalf("kind %v, want Missing", got.Kind)
		}
	})

	t.Run("Deferred: unindexed embedded package under IndexedOnly", func(t *testing.T) {
		src := &fakeSource{indexes: map[string]*index.Index{"bar": bar}, loadable: map[string]*index.Index{"x/inner": inner}}
		s := New(src)
		ref := refFor(t, s, "bar", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Deferred {
			t.Fatalf("kind %v, want Deferred", got.Kind)
		}
		if len(src.loaded) != 0 {
			t.Fatalf("IndexedOnly loaded %v", src.loaded)
		}
	})

	t.Run("AllowIndex loads the embedded package", func(t *testing.T) {
		src := &fakeSource{indexes: map[string]*index.Index{"bar": bar}, loadable: map[string]*index.Index{"x/inner": inner}}
		s := New(src)
		ref := refFor(t, s, "bar", "S")
		got := s.FieldPathOf(ref, "X", AllowIndex)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
		if diff := cmp.Diff([]string{"x/inner"}, src.loaded); diff != "" {
			t.Fatalf("loaded: %s", diff)
		}
	})

	t.Run("Ident input resolves the declaration itself", func(t *testing.T) {
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": foo}})
		ref := TypeRef{Expr: &ast.Ident{Name: "D"}, Ctx: DeclContext{Pkg: "foo"}}
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("wrapped base type peels to its struct", func(t *testing.T) {
		ix := indexOf(t, `package foo
type S (struct{ X int })
type P *struct{ Y int }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
		pref := refFor(t, s, "foo", "P")
		got = s.FieldPathOf(pref, "Y", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("caller binds do not leak into another declaration", func(t *testing.T) {
		ix := indexOf(t, `package foo
type T struct{ X int }
type E struct{ T }
type G[T any] struct{ E }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "G")
		ref.Ctx.Binds = map[string]ast.Expr{"T": &ast.Ident{Name: "int"}}
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found — E's own type T is not G's parameter", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("package clause beats the path basename", func(t *testing.T) {
		odd := indexOf(t, `package zzz
type T struct{ X int }
`)
		bar := indexOf(t, `package bar
import "x/odd"
type S struct{ zzz.T }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"bar": bar, "x/odd": odd}})
		ref := refFor(t, s, "bar", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("dot-imported embedded type resolves", func(t *testing.T) {
		bar := indexOf(t, `package bar
import . "x/inner"
type S struct{ T }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"bar": bar, "x/inner": inner}})
		ref := refFor(t, s, "bar", "S")
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Found {
			t.Fatalf("kind %v, want Found", got.Kind)
		}
		if diff := cmp.Diff([]int{0, 0}, got.Val.Index); diff != "" {
			t.Fatal(diff)
		}
	})

	t.Run("bound type parameter contributes no fields", func(t *testing.T) {
		ix := indexOf(t, `package foo
type E struct{ X int }
type S struct{ T }
`)
		s := New(&fakeSource{indexes: map[string]*index.Index{"foo": ix}})
		ref := refFor(t, s, "foo", "S")
		ref.Ctx.Binds = map[string]ast.Expr{"T": &ast.Ident{Name: "E"}}
		got := s.FieldPathOf(ref, "X", IndexedOnly)
		if got.Kind != Missing {
			t.Fatalf("kind %v, want Missing", got.Kind)
		}
	})
}
