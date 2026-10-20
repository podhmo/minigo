package syntax

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestLocalScopes pins which uses of the name x see a local binding.
// Every selector in src is a probe: x.Loc's x must be local, x.Imp's the
// import.
func TestLocalScopes(t *testing.T) {
	src := `package p

import x "strings"

type T[P any] struct{}

func (r T[x]) M() { _ = x.Loc } // a receiver type parameter

func Sig(x int) x.Imp { return x.Loc } // the result type is outside the body

func Def() {
	x := x.Imp() // the RHS is evaluated before x is in scope
	_ = x.Loc
	{
		var x = x.Loc // the outer local x
		_ = x.Loc
	}
	_ = x.Loc
}

func Var() {
	var x = x.Imp // like :=, a var is in scope after its spec
	_ = x.Loc
}

func Clauses(ch chan int, v any) {
	if x := 1; x.Loc {
	} else {
		_ = x.Loc
	}
	_ = x.Imp
	for x := 0; x.Loc; {
	}
	switch x := v.(type) {
	case int:
		_ = x.Loc
	}
	select {
	case x := <-ch:
		_ = x.Loc
	default:
		_ = x.Imp
	}
	for _, x := range []int{} {
		_ = x.Loc
	}
	_ = x.Imp
}

func Lit() {
	f := func(x int) int { return x.Loc }
	_ = f
	_ = x.Imp
}

func Gen[x any]() { _ = x.Loc }

func Typ() {
	type x struct{ next *x }
	_ = x.Loc
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ls := NewLocalScopes(f)
	var want, got []string
	ast.Inspect(f, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := se.X.(*ast.Ident)
		if !ok || id.Name != "x" {
			return true
		}
		line := fset.Position(id.Pos()).Line
		label := func(local bool) string {
			if local {
				return fmt.Sprintf("%d %s: local", line, se.Sel.Name)
			}
			return fmt.Sprintf("%d %s: import", line, se.Sel.Name)
		}
		want = append(want, label(se.Sel.Name == "Loc"))
		got = append(got, label(ls.At(id.Name, id.Pos())))
		return true
	})
	if len(want) == 0 {
		t.Fatal("no probes")
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("scopes (-want +got):\n%s", diff)
	}
}
