package minigo

import (
	"go/ast"
	"go/token"
	"strings"
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
	// same represented *time.Timer — and a *time.Timer synthesized the
	// ordinary way keys alike: one interned type per represented type.
	a := e.typeIDOf(hostPtrTd("minigo.timerChanBox", func() any { return &timerChanBox{} }))
	b := e.typeIDOf(hostPtrTd("minigo.afterFuncTimer", func() any { return &afterFuncTimer{} }))
	if a != b {
		t.Errorf("timer proxies key differently:\n a=%q\n b=%q", a, b)
	}
	c := e.typeIDOf(hostPtrTd("time.Timer", func() any { return &time.Timer{} }))
	if a != c {
		t.Errorf("proxy *time.Timer keys differently from the real one:\n a=%q\n c=%q", a, c)
	}
	// ...and the carrier must not double-wrap: **time.Timer is another
	// type with a key of its own.
	pp := &runtime.TypeDef{Kind: runtime.KindPointer,
		Elem: hostPtrTd("time.Timer", func() any { return &time.Timer{} })}
	if a == e.typeIDOf(pp) {
		t.Errorf("**time.Timer keys like *time.Timer: %q", a)
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
	// an anonymous typedef inside a generic function inherits the
	// enclosing Binds as resolution context — NOT its own arguments:
	// `use[[]int]` inside wrap[int] and wrap[string] is one type and
	// must share its instantiation-cache key.
	withBinds := func(bv runtime.Value) *runtime.TypeDef {
		return &runtime.TypeDef{
			Kind:  runtime.KindSlice,
			Elem:  runtime.BasicTypedef("int"),
			Binds: map[string]runtime.Value{"T": bv},
		}
	}
	if a, b := e.typeIDOf(withBinds(runtime.BasicTypedef("int"))), e.typeIDOf(withBinds(runtime.BasicTypedef("string"))); a != b {
		t.Errorf("enclosing generic's binds split an anonymous typedef's key: %q vs %q", a, b)
	}
}

// vtdForKey is the Value-typedef for the anonymous-composition case.
func vtdForKey(t *testing.T, e *Engine) *runtime.TypeDef {
	t.Helper()
	return hostType("sync/atomic.Value", func() any { return &atomicBox{} })
}

// the M4-3b contract: one session holds ONE canonical descriptor per
// TypeID, and a forked session's table never aliases the parent's.
func TestInternTypedef(t *testing.T) {
	e := NewEngine(".")

	a := hostPtrTd("minigo.timerChanBox", func() any { return &timerChanBox{} })
	b := hostPtrTd("minigo.afterFuncTimer", func() any { return &afterFuncTimer{} })
	if e.internTypedef(a) != e.internTypedef(b) {
		t.Error("two carriers of *time.Timer did not converge on one typedef")
	}
	// the winner is stable across repeated queries.
	if e.internTypedef(b) != e.internTypedef(a) {
		t.Error("the interned winner changed between queries")
	}

	// a typedef with no identity passes through uninterned.
	bare := &runtime.TypeDef{Kind: runtime.KindStruct}
	if e.internTypedef(bare) != bare {
		t.Error("unidentifiable typedef was interned")
	}

	// a forked session holds its own table: same identity, own winner.
	s := e.NewSession()
	sa := hostPtrTd("time.Timer", func() any { return &time.Timer{} })
	if got := s.internTypedef(sa); got != sa {
		t.Error("the session inherited its parent's interned typedef")
	}
	// ...and interning there does not rewrite the parent's winner.
	if e.internTypedef(a) != a {
		t.Error("session interning rewrote the parent's table")
	}
}

// M4-3c: a bound typedef is the canonical descriptor for its TypeID —
// a synthesized twin (hostTypedefOf-shaped) resolves to it through
// internTypedef, so the bind entry and the synth entry meet.
func TestInternTypedefBoundConverges(t *testing.T) {
	e := NewEngine(".")
	bound := hostType("example.com/x.Widget", func() any { return &strings.Builder{} })
	e.Bind("example.com/x", map[string]runtime.Value{"Widget": bound})

	twin := hostType("example.com/x.Widget", func() any { return &strings.Builder{} })
	if got := e.internTypedef(twin); got != bound {
		t.Errorf("synthesized twin did not converge on the bound typedef: %p vs %p", got, bound)
	}
}

// two packages can share a package-name and a type-name: identity is
// the owning package's PATH, not the name's spelling.
func TestInternTypedefSameNamedPackagesStayDistinct(t *testing.T) {
	e := NewEngine(".")
	a := &runtime.TypeDef{Name: "dup.T", Kind: runtime.KindStruct,
		Pkg: &runtime.Package{Path: "example.com/a/dup", Name: "dup"}}
	b := &runtime.TypeDef{Name: "dup.T", Kind: runtime.KindStruct,
		Pkg: &runtime.Package{Path: "example.com/b/dup", Name: "dup"}}
	if e.typeIDOf(a) == e.typeIDOf(b) {
		t.Fatalf("same-named types of different packages share a key: %q", e.typeIDOf(a))
	}
	if e.internTypedef(a) == e.internTypedef(b) {
		t.Error("different packages' same-named typedefs interned together")
	}
}

// a re-Bind of the same type re-registers the descriptor: the newer
// typedef becomes canonical and the package sees it.
func TestInternTypedefRebindRefreshes(t *testing.T) {
	e := NewEngine(".")
	first := hostType("example.com/x.Widget", func() any { return &strings.Builder{} })
	second := hostType("example.com/x.Widget", func() any { return &strings.Reader{} })
	e.Bind("example.com/x", map[string]runtime.Value{"Widget": first})
	e.Bind("example.com/x", map[string]runtime.Value{"Widget": second})
	if e.internTypedef(first) != second {
		t.Error("re-Bound typedef did not become canonical")
	}
}

// two publishes may interleave interning: bindPublishLocked runs under
// e.mu but bindInternTypedefs spells TypeIDs outside it. An older
// publish whose interning lands after a newer one must not roll the
// TypeID back to its descriptor.
func TestInternTypedefStragglerRebindKeepsNewest(t *testing.T) {
	e := NewEngine(".")
	first := hostType("example.com/x.Widget", func() any { return &strings.Builder{} })
	second := hostType("example.com/x.Widget", func() any { return &strings.Reader{} })
	p1 := e.bindTable("example.com/x", map[string]runtime.Value{"Widget": first})
	p2 := e.bindTable("example.com/x", map[string]runtime.Value{"Widget": second})
	e.mu.Lock()
	keys1, seq1 := e.bindPublishLocked("example.com/x", p1)
	keys2, seq2 := e.bindPublishLocked("example.com/x", p2)
	e.mu.Unlock()
	// the newer bind's interning reaches the table before the older's
	e.bindInternTypedefs(p2, keys2, seq2)
	e.bindInternTypedefs(p1, keys1, seq1)
	if diff := cmp.Diff(true, e.internTypedef(first) == second); diff != "" {
		t.Errorf("straggling older bind rolled the interned typedef back (-want +got):\n%s", diff)
// Two functions' same-named local types are different types: the anon
// composite `[]L` must not collide on their shared spelling, or an
// instance cache keyed on it binds the wrong local decl.
func TestTypeIDAnonLocalLeafsDistinct(t *testing.T) {
	e := NewEngine(".")
	pkg := &runtime.Package{Path: "example.com/mod"}
	f := &syntax.File{}
	localSlice := func(pos token.Pos) *runtime.TypeDef {
		l := srcTd(pkg, f, pos, "L")
		return &runtime.TypeDef{
			Kind: runtime.KindSlice, Pkg: pkg, File: f,
			Anon:       &ast.ArrayType{Elt: ast.NewIdent("L")},
			LocalTypes: map[string]*runtime.TypeDef{"L": l},
		}
	}
	if e.typeIDOf(localSlice(10)) == e.typeIDOf(localSlice(20)) {
		t.Errorf("[]L keys alike across decl sites: %q", e.typeIDOf(localSlice(10)))
	}
	once := e.typeIDOf(localSlice(10))
	if e.typeIDOf(localSlice(10)) != once {
		t.Error("same local decl keys differently")
	}
	// locals in scope do not disturb leafs that don't use them.
	leaf := func(lts map[string]*runtime.TypeDef) *runtime.TypeDef {
		return &runtime.TypeDef{
			Kind: runtime.KindSlice, Pkg: pkg, File: f,
			Anon:       &ast.ArrayType{Elt: ast.NewIdent("int")},
			LocalTypes: lts,
		}
	}
	if a, b := e.typeIDOf(leaf(nil)), e.typeIDOf(leaf(map[string]*runtime.TypeDef{"L": srcTd(pkg, f, 10, "L")})); a != b {
		t.Errorf("[]int keys differ by unused locals: %q vs %q", a, b)
	}
}
