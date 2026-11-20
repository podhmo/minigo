package typedef

import (
	"go/ast"
	"strings"
	"testing"

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
	if !strings.Contains(err.Error(), "cannot resolve type Missing") ||
		!strings.Contains(err.Error(), "(resolving example.com/m.Start)") {
		t.Fatalf("want the resolution walk in the error, got %v", err)
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

	_, err := r.peelAliasTd(tdA, respath{respathKey(tdA)})
	if err == nil {
		t.Fatal("expected a cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") ||
		!strings.Contains(err.Error(), "example.com/m.A -> example.com/m.B") {
		t.Fatalf("want the cycle and the walk, got %v", err)
	}
}
