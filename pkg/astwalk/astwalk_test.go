package astwalk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const src = `package p

import "fmt"

type T struct{ X int }

func F() {
	fmt.Println(T{X: 1})
	n := fmt.Sprintf
	_ = n
}
`

func parse(t *testing.T) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEachYieldsTypedNodesInOrder(t *testing.T) {
	f := parse(t)
	var got []string
	for se := range Each[*ast.SelectorExpr](f) {
		x, ok := se.X.(*ast.Ident)
		if !ok {
			continue
		}
		got = append(got, x.Name+"."+se.Sel.Name)
	}
	want := []string{"fmt.Println", "fmt.Sprintf"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Error(diff)
	}
}

func TestEachStopsOnBreak(t *testing.T) {
	f := parse(t)
	var got []string
	for d := range Each[*ast.GenDecl](f) {
		got = append(got, d.Tok.String())
		break
	}
	if diff := cmp.Diff([]string{"import"}, got); diff != "" {
		t.Error(diff)
	}
}

func TestEachNilRootAndNoMatch(t *testing.T) {
	for range Each[*ast.FuncDecl](nil) {
		t.Error("nil root yielded a node")
	}
	for range Each[*ast.ChanType](parse(t)) {
		t.Error("no *ast.ChanType in source")
	}
}
