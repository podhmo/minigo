package minigo

import (
	"go/ast"
	"go/token"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// the M4-3a counterexamples: typedefs that share carriers or shapes but
// name different types must key differently, and typedefs minted through
// different synthesis paths for the SAME type must key alike.

func hostPtrTd(name string, new func() any) *runtime.TypeDef {
	// what vm.hostTypedefOf mints for a named host struct type.
	return &runtime.TypeDef{
		Kind: runtime.KindPointer,
		Elem: &runtime.TypeDef{Name: name, Kind: runtime.KindStruct, HostNew: new},
	}
}

func srcTd(pkg *runtime.Package, file *syntax.File, pos token.Pos, name string) *runtime.TypeDef {
	return &runtime.TypeDef{
		Kind: runtime.KindStruct,
		Pkg:  pkg,
		File: file,
		Spec: &ast.TypeSpec{Doc: nil, Name: &ast.Ident{Name: name, NamePos: pos}, Type: &ast.StructType{}},
	}
}

func TestTypeIDHostKeyIsTheRepresentedType(t *testing.T) {
	e := NewEngine(".")

	// atomic.Value and atomic.Pointer[T] share the *atomicBox carrier:
	// the key comes from the declared represented name, not the box.
	vtd := hostType("sync/atomic.Value", func() any { return &atomicBox{} })
	ptd := &runtime.TypeDef{
		Name: "sync/atomic.Pointer", Kind: runtime.KindStruct,
		TParams: []string{"T"}, Binds: map[string]runtime.Value{"T": runtime.BasicTypedef("int")},
		HostNew: func() any { return &atomicBox{} },
	}
	if diff := cmp.Diff(false, e.typeIDOf(vtd) == e.typeIDOf(ptd)); diff != "" {
		t.Errorf("Value vs Pointer key%s", diff)
	}

	// and Pointer[int] vs Pointer[string] differ only in their args.
	ps := &runtime.TypeDef{
		Name: "sync/atomic.Pointer", Kind: runtime.KindStruct,
		TParams: []string{"T"}, Binds: map[string]runtime.Value{"T": runtime.BasicTypedef("string")},
		HostNew: func() any { return &atomicBox{} },
	}
	if e.typeIDOf(ptd) == e.typeIDOf(ps) {
		t.Errorf("Pointer[int] and Pointer[string] key alike: %q", e.typeIDOf(ptd))
	}

	// timerChanBox and afterFuncTimer are different carriers for the
	// same represented *time.Timer — the keys must agree.
	a := e.typeIDOf(hostPtrTd("minigo.timerChanBox", func() any { return &timerChanBox{} }))
	b := e.typeIDOf(hostPtrTd("minigo.afterFuncTimer", func() any { return &afterFuncTimer{} }))
	if a != b {
		t.Errorf("timer proxies key differently:\n a=%q\n b=%q", a, b)
	}
	// ...but they are not the same as a real *time.Timer typedef.
	c := e.typeIDOf(hostPtrTd("time.Timer", func() any { return &time.Timer{} }))
	if a == c {
		t.Errorf("proxy *time.Timer keys like the real one: %q", c)
	}
}

func TestTypeIDSourceKeyCarriesVersionAndArgs(t *testing.T) {
	e := NewEngine(".")
	pkg := &runtime.Package{Path: "example.com/mod"}
	f1, f2 := &syntax.File{}, &syntax.File{}

	a := srcTd(pkg, f1, 10, "S")
	// the same decl in the same parse keys alike.
	if e.typeIDOf(a) != e.typeIDOf(srcTd(pkg, f1, 10, "S")) {
		t.Errorf("same decl, same parse keys differently")
	}
	// a re-parse is a new source version: same pos, different File.
	if e.typeIDOf(a) == e.typeIDOf(srcTd(pkg, f2, 10, "S")) {
		t.Errorf("different parses key alike: %q", e.typeIDOf(a))
	}
	// a different decl position is a different type.
	if e.typeIDOf(a) == e.typeIDOf(srcTd(pkg, f1, 20, "S")) {
		t.Errorf("different decl positions key alike: %q", e.typeIDOf(a))
	}
	// a different package path is a different type.
	if e.typeIDOf(a) == e.typeIDOf(srcTd(&runtime.Package{Path: "other"}, f1, 10, "S")) {
		t.Errorf("different package paths key alike")
	}
	// type args distinguish instantiations of one decl.
	intB := map[string]runtime.Value{"T": runtime.BasicTypedef("int")}
	strB := map[string]runtime.Value{"T": runtime.BasicTypedef("string")}
	gi := &runtime.TypeDef{Kind: runtime.KindStruct, Pkg: pkg, File: f1, Spec: a.Spec, TParams: []string{"T"}, Binds: intB}
	gs := &runtime.TypeDef{Kind: runtime.KindStruct, Pkg: pkg, File: f1, Spec: a.Spec, TParams: []string{"T"}, Binds: strB}
	if e.typeIDOf(gi) == e.typeIDOf(gs) {
		t.Errorf("G[int] and G[string] key alike: %q", e.typeIDOf(gi))
	}
	// ...while a bare G and G[int] differ too (unbound vs bound).
	gb := &runtime.TypeDef{Kind: runtime.KindStruct, Pkg: pkg, File: f1, Spec: a.Spec, TParams: []string{"T"}}
	if e.typeIDOf(gi) == e.typeIDOf(gb) {
		t.Errorf("G and G[int] key alike: %q", e.typeIDOf(gi))
	}
}

func TestTypeIDAnonymousIsStructural(t *testing.T) {
	e := NewEngine(".")
	pkg := &runtime.Package{Path: "example.com/mod"}
	f := &syntax.File{}
	s := srcTd(pkg, f, 10, "S")
	r := srcTd(pkg, f, 20, "R")

	ps := &runtime.TypeDef{Kind: runtime.KindPointer, Elem: s}
	// same element → same key.
	if e.typeIDOf(ps) != e.typeIDOf(&runtime.TypeDef{Kind: runtime.KindPointer, Elem: s}) {
		t.Errorf("two *S typedefs key differently")
	}
	// different element → different key.
	if e.typeIDOf(ps) == e.typeIDOf(&runtime.TypeDef{Kind: runtime.KindPointer, Elem: r}) {
		t.Errorf("*S and *R key alike: %q", e.typeIDOf(ps))
	}
	// different kind over the same element → different key.
	if e.typeIDOf(ps) == e.typeIDOf(&runtime.TypeDef{Kind: runtime.KindSlice, Elem: s}) {
		t.Errorf("*S and []S key alike")
	}
	// a host element composes the same way.
	hp := &runtime.TypeDef{Kind: runtime.KindPointer, Elem: vtdForKey(t, e)}
	if string(e.typeIDOf(hp)) == "" {
		t.Errorf("anonymous ptr over host elem produced no key")
	}
}

// vtdForKey is the Value-typedef for the anonymous-composition case.
func vtdForKey(t *testing.T, e *Engine) *runtime.TypeDef {
	t.Helper()
	return hostType("sync/atomic.Value", func() any { return &atomicBox{} })
}
