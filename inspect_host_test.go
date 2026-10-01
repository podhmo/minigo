package minigo_test

// inspect_host_test.go — host-side (Go API) coverage for the inspect
// layer: the same views scripts reach through intrinsics, consumed
// directly from Go. See docs/sketch/plan-package-introspection.md
// round-4 notes.

import (
	"context"
	"go/ast"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	xinspect "github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/runtime"
)

// lookupType builds a decl view for a named type in an indexed package.
func lookupType(t *testing.T, p *runtime.Package, name string) *xinspect.Decl {
	t.Helper()
	if p.Index == nil {
		t.Fatalf("package %s has no index", p.Path)
	}
	td, ok := p.Index.Types[name]
	if !ok || td.Decl == nil {
		t.Fatalf("no type decl %s in %s", name, p.Path)
	}
	return xinspect.NewDecl(p, td.Decl)
}

// fieldTypes maps a struct decl's fields by their written type spelling.
func fieldTypes(t *testing.T, d *xinspect.Decl) map[string]*xinspect.TypeExpr {
	t.Helper()
	fs, err := xinspect.FieldsOf(d)
	if err != nil {
		t.Fatalf("FieldsOf(%s): %v", d.Name, err)
	}
	out := map[string]*xinspect.TypeExpr{}
	for _, f := range fs {
		out[f.Type.Text] = f.Type
	}
	return out
}

// TestEngineSourceOf: host code reaches the source decls behind a bound
// package — Package keeps answering the shadow, SourceOf parses GOROOT.
func TestEngineSourceOf(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()

	bound, err := e.Package(ctx, "strings")
	if err != nil {
		t.Fatal(err)
	}
	if bound.Index != nil {
		t.Fatal("bound strings should carry no index")
	}

	src, err := e.SourceOf(ctx, "strings")
	if err != nil {
		t.Fatal(err)
	}
	if src == bound {
		t.Error("SourceOf returned the bound package, not the source")
	}
	if src.Index == nil {
		t.Fatal("source strings has no index")
	}
	if !src.Standard {
		t.Error("source strings not marked standard")
	}
	d := lookupType(t, src, "Builder")
	if d.File == "" || d.Pos == "" {
		t.Errorf("source decl lacks position info: %+v", d)
	}
	if fs, err := xinspect.FieldsOf(d); err != nil || len(fs) == 0 {
		t.Errorf("source decl has no fields: %v", err)
	}

	// Unbound path: nothing to bypass — the canonical package comes back.
	canon, err := e.Package(ctx, "sync")
	if err != nil {
		t.Fatal(err)
	}
	via, err := e.SourceOf(ctx, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if via != canon {
		t.Error("unbound SourceOf should return the canonical package")
	}
}

// TestSigFields: ParamFields/ResultFields unbox the FFI slices into
// []*Field — the host-side counterpart of FieldsOf.
func TestSigFields(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	fd, ok := p.Index.Funcs["Hello"] // func Hello(s string) string
	if !ok {
		t.Fatal("no func decl Hello")
	}
	sig, err := xinspect.SignatureOf(xinspect.NewDecl(p, fd))
	if err != nil {
		t.Fatal(err)
	}
	params := sig.ParamFields()
	if len(params) != 1 {
		t.Fatalf("params: got %d", len(params))
	}
	if diff := cmp.Diff([]string{"s"}, params[0].Names); diff != "" {
		t.Errorf("param names mismatch (-want +got):\n%s", diff)
	}
	if params[0].Type == nil || params[0].Type.Text != "string" {
		t.Errorf("param type: %+v", params[0].Type)
	}
	results := sig.ResultFields()
	if len(results) != 1 || results[0].Type == nil || results[0].Type.Text != "string" {
		t.Errorf("results: %+v", results)
	}

	// A method sig keeps the receiver on Recv; a nil slice unboxes to nil.
	var empty xinspect.Sig
	if empty.ParamFields() != nil || empty.ResultFields() != nil {
		t.Error("nil slices should unbox to nil")
	}
	md := lookupType(t, p, "User")
	ms, err := xinspect.MethodsOf(md)
	if err != nil {
		t.Fatal(err)
	}
	var greet *xinspect.Decl
	for _, m := range ms {
		if m.Name == "Greet" {
			greet = m
		}
	}
	gsig, err := xinspect.SignatureOf(greet)
	if err != nil {
		t.Fatal(err)
	}
	if gsig.Recv == nil || gsig.Recv.Type == nil || gsig.Recv.Type.Text != "*User" {
		t.Errorf("recv: %+v", gsig.Recv)
	}
}

// TestCanonicalName: the canonical "*import/path.Name" identity spelling.
func TestCanonicalName(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}

	qualify := func(name string) string { return p.Path + "." + name }

	got := map[string]string{}
	for text, te := range fieldTypes(t, lookupType(t, p, "User")) {
		got[text] = te.CanonicalName()
	}
	want := map[string]string{
		"string":          "string",              // builtin
		"int":             "int",                 // builtin
		"*Base":           "*" + qualify("Base"), // pointer to declared type
		"strings.Builder": "strings.Builder",     // selector through the import table
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("User fields (-want +got):\n%s", diff)
	}

	// Composites carry no package-qualified identity; a generic
	// instantiation's SymbolID is unreachable (round-3 audit).
	got = map[string]string{}
	for text, te := range fieldTypes(t, lookupType(t, p, "Rec")) {
		got[text] = te.CanonicalName()
	}
	want = map[string]string{
		"map[string]int": "",
		"chan string":    "",
		"func(int) bool": "",
		"Speaker":        qualify("Speaker"),
		"MyInt":          qualify("MyInt"),
		"Pair[int]":      "",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Rec fields (-want +got):\n%s", diff)
	}

	// Host-backed exprs follow the same contract: named types qualify,
	// pointers peel, unnamed composites report "".
	for name, c := range map[string]struct {
		te   *xinspect.TypeExpr
		want string
	}{
		"named":   {xinspect.NewHostType(reflect.TypeOf(strings.Builder{})), "strings.Builder"},
		"ptr":     {xinspect.NewHostType(reflect.TypeOf(&strings.Builder{})), "*strings.Builder"},
		"builtin": {xinspect.NewHostType(reflect.TypeOf(0)), "int"},
		"slice":   {xinspect.NewHostType(reflect.TypeOf([]byte(nil))), ""},
		"nil":     {nil, ""},
	} {
		if got := c.te.CanonicalName(); got != c.want {
			t.Errorf("%s: CanonicalName() = %q, want %q", name, got, c.want)
		}
	}
}

// TestTypeExprSub: Sub re-wraps sub-expressions Children() cannot reach —
// the generic origin under an IndexExpr.
func TestTypeExprSub(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	ip := fieldTypes(t, lookupType(t, p, "Rec"))["Pair[int]"]
	ix, ok := ip.Expr().(*ast.IndexExpr)
	if !ok {
		t.Fatalf("Pair[int] expr is %T", ip.Expr())
	}
	base := ip.Sub(ix.X)
	if got, want := base.CanonicalName(), p.Path+".Pair"; got != want {
		t.Errorf("generic origin: got %q, want %q", got, want)
	}
	if ip.Sub(nil) != nil {
		t.Error("Sub(nil) should return nil")
	}
}
