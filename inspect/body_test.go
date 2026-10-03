package inspect_test

import (
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

func TestBodyShapes(t *testing.T) {
	fset := token.NewFileSet()
	sf, err := syntax.ParseFile(fset, "body.go", []byte(`package p
 type T struct{}
 func Empty()
 func (t *T) Method() { var x []map[string]*T; _=x; f:=func() { select { default: } }; _=f }
 `))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Build([]*syntax.File{sf})
	if err != nil {
		t.Fatal(err)
	}
	p := &runtime.Package{Path: "example/p", Fset: fset, Files: []*syntax.File{sf}, Index: ix}
	empty, err := inspect.BodyOf(inspect.NewDecl(p, ix.Funcs["Empty"]))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(true, empty == nil); diff != "" {
		t.Error(diff)
	}
	for _, d := range []*inspect.Decl{nil, inspect.NewHostDecl(p, "host", nil, nil), inspect.NewDecl(p, ix.Types["T"].Decl)} {
		if _, err := inspect.BodyOf(d); err == nil {
			t.Error("expected non-source-function error")
		}
	}
	owner := inspect.NewDecl(p, ix.Types["T"].Methods["Method"])
	body, err := inspect.BodyOf(owner)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	var walk func(*inspect.Node)
	walk = func(n *inspect.Node) {
		kinds[n.Kind]++
		if diff := cmp.Diff(true, n.Owner == owner && n.Pos != ""); diff != "" {
			t.Errorf("lost declaration anchor: %s", diff)
		}
		if n.Kind == "ArrayType" && n.Type == nil {
			t.Error("missing local explicit type")
		}
		for _, c := range n.ChildNodes() {
			walk(c)
		}
	}
	walk(body)
	for kind, want := range map[string]int{"FuncLit": 1, "SelectStmt": 1, "CommClause": 1, "ArrayType": 1, "MapType": 1} {
		if diff := cmp.Diff(want, kinds[kind]); diff != "" {
			t.Errorf("%s: %s", kind, diff)
		}
	}
}
