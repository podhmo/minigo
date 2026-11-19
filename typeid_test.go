package minigo

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
	"time"

	stdatomic "github.com/podhmo/minigo/internal/stdlib/atomic"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/bytecode"
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

	// atomic.Value and atomic.Pointer[T] share the *stdatomic.AtomicBox carrier:
	// the key comes from the declared represented name, not the box.
	vtd := hostType("sync/atomic.Value", func() any { return &stdatomic.AtomicBox{} })
	ptd := &runtime.TypeDef{
		Name: "sync/atomic.Pointer", Kind: runtime.KindStruct,
		TParams: []string{"T"}, Binds: map[string]runtime.Value{"T": runtime.BasicTypedef("int")},
		HostNew: func() any { return &stdatomic.AtomicBox{} },
	}
	if diff := cmp.Diff(false, e.typeIDOf(vtd) == e.typeIDOf(ptd)); diff != "" {
		t.Errorf("Value vs Pointer key%s", diff)
	}

	// and Pointer[int] vs Pointer[string] differ only in their args.
	ps := &runtime.TypeDef{
		Name: "sync/atomic.Pointer", Kind: runtime.KindStruct,
		TParams: []string{"T"}, Binds: map[string]runtime.Value{"T": runtime.BasicTypedef("string")},
		HostNew: func() any { return &stdatomic.AtomicBox{} },
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
	return hostType("sync/atomic.Value", func() any { return &stdatomic.AtomicBox{} })
}

// M4-7a: a deferred fact's answer keeps the expression's own shape —
// `*int` is a pointer typedef (resolveTypeRef's reference resolution
// strips the star), and parens count their inner star the same way.
func TestDeferredTypeOfKeepsPointerShape(t *testing.T) {
	e := NewEngine(".")
	ctx := &runtime.TypeDef{Pkg: &runtime.Package{Path: "main"}}
	for name, expr := range map[string]ast.Expr{
		"star":  &ast.StarExpr{X: ast.NewIdent("int")},
		"paren": &ast.ParenExpr{X: &ast.StarExpr{X: ast.NewIdent("int")}},
	} {
		td, err := e.deferredTypeOf(&bytecode.CallSite{Expr: expr}, ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if td.Kind != runtime.KindPointer {
			t.Fatalf("%s: resolved to %v, want a pointer typedef", name, td.Kind)
		}
	}
	td, err := e.deferredTypeOf(&bytecode.CallSite{Expr: ast.NewIdent("int")}, ctx)
	if err != nil {
		t.Fatalf("ident: %v", err)
	}
	if td.Kind == runtime.KindPointer {
		t.Fatalf("plain ident gained a pointer layer")
	}
	// **int: the inner pointer's Anon is the `*int` expr — tagging every
	// level with the whole `**int` tree would make TypIdentical equate
	// the result with its own element.
	inner := &ast.StarExpr{X: ast.NewIdent("int")}
	outer := &ast.StarExpr{X: inner}
	dd, err := e.deferredTypeOf(&bytecode.CallSite{Expr: outer}, ctx)
	if err != nil {
		t.Fatalf("starstar: %v", err)
	}
	el := dd.Elem
	if el == nil || el.Kind != runtime.KindPointer {
		t.Fatalf("**int's element is %v, want a pointer typedef", dd.Elem)
	}
	if dd.Anon != ast.Expr(outer) || el.Anon != ast.Expr(inner) {
		t.Fatalf("pointer levels carry the wrong syntax trees")
	}
	if runtime.TypIdentical(dd, el) {
		t.Fatalf("**int reads identical to its *int element")
	}
	// a pointer over a local type keeps the decl-scope snapshot so its
	// spelling matches pointers minted by typeExpr.
	ltd := &runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: "L"}
	lctx := &runtime.TypeDef{Pkg: ctx.Pkg, LocalTypes: map[string]*runtime.TypeDef{"L": ltd}}
	lp, err := e.deferredTypeOf(&bytecode.CallSite{Expr: &ast.StarExpr{X: ast.NewIdent("L")}}, lctx)
	if err != nil {
		t.Fatalf("local star: %v", err)
	}
	if lp.LocalTypes["L"] != ltd {
		t.Fatalf("*L dropped the local-type snapshot")
	}
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

// M4-6b: the compiled-instantiation key identifies the declaration plus
// each bind's TypeID — copies of one type share a chunk — with pointer
// fallbacks for un-internable typedefs and "" for non-typedef binds.
func TestInstKeyTypeID(t *testing.T) {
	e := NewEngine(".")
	decl := &ast.FuncDecl{}
	file := &syntax.File{}

	sliceOfInt := func() *runtime.TypeDef {
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent("int")}}
	}
	mkFn := func(binds map[string]runtime.Value) *runtime.Function {
		return &runtime.Function{Decl: decl, File: file, Name: "f", Binds: binds}
	}

	// distinct typedef objects of one type share the key — the intern
	// table's whole point.
	ka := e.instKey(mkFn(map[string]runtime.Value{"T": sliceOfInt()}))
	kb := e.instKey(mkFn(map[string]runtime.Value{"T": sliceOfInt()}))
	if ka == "" {
		t.Fatal("instKey returned empty for a spelled anonymous bind")
	}
	if ka != kb {
		t.Fatalf("copies of one type must share the key:\n%s\n%s", ka, kb)
	}
	// a different type binds a different key.
	kc := e.instKey(mkFn(map[string]runtime.Value{"T": &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Elt: ast.NewIdent("string")}}}))
	if kc == ka {
		t.Fatal("[]int and []string binds must key differently")
	}
	// un-internable typedefs key by their own pointer: two placeholders
	// never share a compiled chunk.
	un := func() *runtime.TypeDef { return &runtime.TypeDef{Kind: runtime.KindPointer} }
	ku1, ku2 := e.instKey(mkFn(map[string]runtime.Value{"T": un()})), e.instKey(mkFn(map[string]runtime.Value{"T": un()}))
	if ku1 == "" || ku1 == ku2 {
		t.Fatal("un-internable binds must key by pointer")
	}
	// a non-typedef bind cannot key an instantiation.
	if k := e.instKey(mkFn(map[string]runtime.Value{"T": int64(1)})); k != "" {
		t.Fatalf("non-typedef bind keyed %q, want \"\"", k)
	}
}
