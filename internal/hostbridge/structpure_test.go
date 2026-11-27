package hostbridge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func parsePureTestMethod(t *testing.T, body string) *runtime.Function {
	t.Helper()
	src := "package p\ntype T struct { N int; Values chan int }\n" + body
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			return &runtime.Function{Decl: fn}
		}
	}
	t.Fatal("method not found")
	return nil
}

func TestPureMethodRejectsChannelOperations(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"unary receive", "func (p *T) Read() int { return <-p.Values }\n"},
		{"range receive", "func (p *T) Read() int { for v := range p.Values { return v }; return 0 }\n"},
		{"select receive", "func (p *T) Read() int { select { case v := <-p.Values: return v }; return 0 }\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(false, pureMethod(parsePureTestMethod(t, tc.body))); diff != "" {
				t.Fatalf("pureMethod (-want +got):\n%s", diff)
			}
		})
	}
}
