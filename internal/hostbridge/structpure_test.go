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

func TestRefreshableMethodLimitsResultShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"scalar getter", "func (p *T) Label() int { return p.N }\n", true},
		{"scalar error", "func (p *T) Status() (int, error) { return p.N, nil }\n", true},
		{"struct result", "func (p *T) Child() *T { return p }\n", false},
		{"grouped results", "func (p *T) Pair() (a, b int) { return p.N, p.N }\n", false},
		{"channel receive", "func (p *T) Read() int { return <-p.Values }\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, refreshableMethod(parsePureTestMethod(t, tc.body))); diff != "" {
				t.Fatalf("refreshableMethod (-want +got):\n%s", diff)
			}
		})
	}
}
