package typedef

import (
	"go/ast"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func TestRespathAnnotatesFailure(t *testing.T) {
	r := New(Deps{Builtins: runtime.NewEnv()})
	pkg := &runtime.Package{Path: "example.com/m", Name: "m"}
	from := &runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: "Start", Pkg: pkg}
	_, err := r.ResolveTypeRef(from, ast.NewIdent("Missing"))
	if err == nil {
		t.Fatal("expected a resolution error")
	}
	want := "cannot resolve type Missing (resolving example.com/m.Start)"
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Error(diff)
	}
}

// `type A = B; type B = A` — the peel reports the cycle and names the walk.
func TestRespathReportsAliasCycle(t *testing.T) {
	r := New(Deps{Builtins: runtime.NewEnv()})
	pkg := &runtime.Package{Path: "example.com/m", Name: "m"}
	pkg.Globals = runtime.NewEnv()
	tdA := &runtime.TypeDef{Kind: runtime.KindAlias, Name: "A", Pkg: pkg, Anon: ast.NewIdent("B")}
	tdB := &runtime.TypeDef{Kind: runtime.KindAlias, Name: "B", Pkg: pkg, Anon: ast.NewIdent("A")}
	pkg.Globals.Set("A", tdA)
	pkg.Globals.Set("B", tdB)

	_, err := r.peelAliasTd(tdA, seedPath(tdA))
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	want := "type alias cycle through example.com/m.A (resolving example.com/m.A -> example.com/m.B)"
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Error(diff)
	}
}

// `type T []A; type A = T` — A peels back to T, which is the answer, not
// a cycle: the loop only peels aliases, and T is not one.
func TestRespathAliasIntoWalkedNamedDecl(t *testing.T) {
	r := New(Deps{Builtins: runtime.NewEnv()})
	pkg := &runtime.Package{Path: "example.com/m", Name: "m"}
	pkg.Globals = runtime.NewEnv()
	tdT := &runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: "T", Pkg: pkg, Anon: &ast.ArrayType{Elt: ast.NewIdent("A")}}
	tdA := &runtime.TypeDef{Kind: runtime.KindAlias, Name: "A", Pkg: pkg, Anon: ast.NewIdent("T")}
	pkg.Globals.Set("T", tdT)
	pkg.Globals.Set("A", tdA)

	got, err := r.peelAliasTd(tdA, seedPath(tdT).push(tdA))
	if err != nil {
		t.Fatalf("peel reported a cycle for a legal alias: %v", err)
	}
	if got != tdT {
		t.Errorf("want the named decl T, got %v", got)
	}
}

// A package `type A []int` and a function-local `type A B` both spell
// "example.com/m.A"; the walk must tell them apart by decl, not name.
func TestRespathSameNameDifferentDecls(t *testing.T) {
	pkg := &runtime.Package{Path: "example.com/m", Name: "m"}
	pkgA := &runtime.TypeDef{
		Kind: runtime.KindNamedBasic, Name: "A", Pkg: pkg,
		Spec: &ast.TypeSpec{Name: ast.NewIdent("A")},
	}
	localA := &runtime.TypeDef{
		Kind: runtime.KindNamedBasic, Name: "A", Pkg: pkg,
		Spec: &ast.TypeSpec{Name: ast.NewIdent("A")},
	}
	p := seedPath(pkgA)
	if p.seen(localA) {
		t.Error("a local decl collided with the package decl of the same name")
	}
	if !p.seen(pkgA) {
		t.Error("the seeded decl did not see itself")
	}
}
