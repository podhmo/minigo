package compile

import (
	"go/token"
	"testing"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// selMember must keep the ix.Types[name] != nil verdict for a synthetic
// TypeDeclInfo — a method receiver whose declaration lives outside this
// index (REPL-merged or single-file builds create those with Decl==nil).
// Requiring Decl.Spec.(*ast.TypeSpec) non-nil reported "not a type" for
// a name the index knows is a type.
// Compile-time fact (M0-7): exercises selMember's is-type
// classification directly. docs/next/ja/references/acceptance-set.md.
func TestSelMemberSyntheticTypeEntry(t *testing.T) {
	checkSelMemberSyntheticTypeEntry(t)
}

// The same verdict under forced-deferred facts (M4-7b): selMember is
// the is-type oracle — a Deferred answer still carries the materialized
// package, so the out-of-band question keeps answering while emission
// paths take their CallSite fallbacks.
func TestSelMemberSyntheticTypeEntryDeferred(t *testing.T) {
	defer func(d bool) { DeferFacts = d }(DeferFacts)
	DeferFacts = true
	checkSelMemberSyntheticTypeEntry(t)
}

func checkSelMemberSyntheticTypeEntry(t *testing.T) {
	fset := token.NewFileSet()
	f, err := syntax.ParseFile(fset, "m.go", []byte("package p\nfunc (T) M() {}"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ix, err := index.BuildLenient([]*syntax.File{f})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if ix.Types["T"] == nil || ix.Types["T"].Decl != nil {
		t.Fatalf("fixture shape wrong: Types[T] = %+v", ix.Types["T"])
	}

	ref := &runtime.ImportRef{
		Path: "p",
		Load: func(path string) (*runtime.Package, error) {
			return &runtime.Package{Path: path, Index: ix}, nil
		},
	}
	c := &compiler{}
	if _, _, ok := c.selMember(ref, "T"); !ok {
		t.Fatal("selMember lost the is-type verdict on a synthetic index entry")
	}
	if !c.selIsType(ref, "T") {
		t.Fatal("selIsType lost the is-type verdict on a synthetic index entry")
	}
}
