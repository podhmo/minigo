package compile

import (
	"go/token"
	"testing"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/semantics"
	"github.com/podhmo/minigo/internal/source"
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
			return &runtime.Package{Path: path, PackageImage: runtime.PackageImage{Index: ix}}, nil
		},
	}
	c := &compiler{}
	res := c.selMember(ref, "T")
	if res.Val == nil || (res.Kind != semantics.Found && res.Kind != semantics.Deferred) {
		t.Fatalf("selMember lost the is-type verdict on a synthetic index entry: %v", res.Kind)
	}
	if res := c.selMember(ref, "Nope"); res.Kind != semantics.Missing || res.Val != nil {
		t.Fatalf("selMember on an absent member = %v, want Missing", res.Kind)
	}
	if !c.selIsType(ref, "T") {
		t.Fatal("selIsType lost the is-type verdict on a synthetic index entry")
	}
}

// The same verdict under strict lazy compilation (FuncStrict): an
// unmaterialized ref defers instead of answering, and once the ref is
// materialized to Indexed the IndexedOnly peek still reports the
// synthetic entry's is-type verdict — the strict acceptance job
// exercises this compile-time-fact dependency directly.
func TestStrictLazySelMemberSyntheticTypeEntry(t *testing.T) {
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

	// A package never MarkIndexed reports IndexReady, so the
	// materialized ref answers Found under IndexedOnly.
	p := &runtime.Package{Path: "p", PackageImage: runtime.PackageImage{Index: ix}}
	ref := &runtime.ImportRef{
		Path: "p",
		Load: func(path string) (*runtime.Package, error) {
			return p, nil
		},
	}
	c := &compiler{strictLazy: true}
	if res := c.selMember(ref, "T"); res.Val != nil || res.Kind != semantics.Deferred {
		t.Fatalf("selMember on an unmaterialized ref under IndexedOnly = %v, want Deferred with no Val", res.Kind)
	}
	// Carry the ref to the Indexed stage through the same adapter the
	// fact sites use — compile/ never calls ImportRef.Materialize.
	if res := source.PackageAtIndexed(ref, semantics.AllowIndex); res.Kind != semantics.Found {
		t.Fatalf("PackageAtIndexed: %v (err %v)", res.Kind, res.Err)
	}
	if res := c.selMember(ref, "T"); res.Val == nil {
		t.Fatal("selMember lost the is-type verdict on a synthetic index entry under IndexedOnly")
	}
	if !c.selIsType(ref, "T") {
		t.Fatal("selIsType lost the is-type verdict on a synthetic index entry under IndexedOnly")
	}
}
