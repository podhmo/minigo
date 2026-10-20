package minigo_test

// inspect_host_test.go — host-side (Go API) coverage for the inspect
// layer: the same views scripts reach through intrinsics, consumed
// directly from Go. See docs/sketch/plan-package-introspection.md
// round-4 notes.

import (
	"context"
	"fmt"
	"go/ast"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/index"
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
	if d.File == "" || d.Pos == nil {
		t.Errorf("source decl lacks position info: %+v", d)
	}
	if fs, err := xinspect.FieldsOf(d); err != nil || len(fs) == 0 {
		t.Errorf("source decl has no fields: %v", err)
	}

	// Unbound path: nothing to bypass — the canonical package comes back.
	canon, err := e.Package(ctx, "container/list")
	if err != nil {
		t.Fatal(err)
	}
	via, err := e.SourceOf(ctx, "container/list")
	if err != nil {
		t.Fatal(err)
	}
	if via != canon {
		t.Error("unbound SourceOf should return the canonical package")
	}
}

// TestEngineSourceOfDenied: ModeDeny gates imports, not SourceOf — a
// denied path's source loads whether the path is bound or not, while
// Package keeps refusing it.
func TestEngineSourceOfDenied(t *testing.T) {
	e := minigo.NewEngine(".", minigo.WithPackageModes(map[string]minigo.PackageMode{
		"strings":        minigo.ModeDeny, // bound and denied
		"container/list": minigo.ModeDeny, // unbound and denied
	}))
	ctx := context.Background()

	for _, path := range []string{"strings", "container/list"} {
		src, err := e.SourceOf(ctx, path)
		if err != nil {
			t.Fatalf("SourceOf(%q): %v", path, err)
		}
		if src.Index == nil {
			t.Errorf("SourceOf(%q) has no index", path)
		}
		if _, err := e.Package(ctx, path); err == nil {
			t.Errorf("Package(%q) should keep refusing the denied path", path)
		}
	}
}

// TestEngineBoundOf: the bound-only loader answers the registered
// shadow or nil — the complement of SourceOf's source-only direction.
func TestEngineBoundOf(t *testing.T) {
	e := newEngine(t)

	if p := e.BoundOf("strings"); p == nil {
		t.Fatal("bound strings missing")
	} else {
		if p.Path != "strings" || !p.Standard {
			t.Errorf("bad bound meta: %s standard=%t", p.Path, p.Standard)
		}
		if p.Index != nil {
			t.Error("bound package should carry no index")
		}
		// the bound object is the one imports answer
		if canon, err := e.Package(context.Background(), "strings"); err != nil || canon != p {
			t.Error("BoundOf is not the canonical bound object")
		}
	}
	// unbound paths report nil — never the source Package would load
	if p := e.BoundOf("container/list"); p != nil {
		t.Error("unbound path answered")
	}
	if p := e.BoundOf("./testdata/inspectpkg"); p != nil {
		t.Error("dir string answered")
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

	// NumParams/NumResults flatten field-group names into arity: `a, b
	// int` is two params in one written field, `(q, r int)` two results
	// in one; a variadic counts once.
	two, err := xinspect.SignatureOf(xinspect.NewDecl(p, p.Index.Funcs["TwoArgs"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(two.ParamFields()) != 2 || two.NumParams() != 3 {
		t.Errorf("TwoArgs: %d fields, NumParams %d", len(two.ParamFields()), two.NumParams())
	}
	bare, err := xinspect.SignatureOf(xinspect.NewDecl(p, p.Index.Funcs["PosArgs"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.ParamFields()) != 2 || bare.NumParams() != 2 {
		t.Errorf("PosArgs: %d fields, NumParams %d", len(bare.ParamFields()), bare.NumParams())
	}
	dv, err := xinspect.SignatureOf(xinspect.NewDecl(p, p.Index.Funcs["Divmod"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(dv.ResultFields()) != 1 || dv.NumResults() != 2 {
		t.Errorf("Divmod: %d fields, NumResults %d", len(dv.ResultFields()), dv.NumResults())
	}

	// A method sig keeps the receiver on Recv; a nil slice unboxes to nil.
	var empty xinspect.Sig
	if empty.ParamFields() != nil || empty.ResultFields() != nil {
		t.Error("nil slices should unbox to nil")
	}
	if empty.NumParams() != 0 || empty.NumResults() != 0 {
		t.Error("empty sig should report zero arity")
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

// TestCommentsOf: a file's comments enumerate flat in source order —
// Free marks the groups no declaration claims.
func TestCommentsOf(t *testing.T) {
	e := newEngine(t)
	p, err := e.Package(context.Background(), "./testdata/comments")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(p.Files))
	}
	cs := xinspect.CommentsOf(xinspect.NewFile(p, p.Files[0]))
	var got []string
	for _, c := range cs {
		line := 0
		if c.Pos != nil {
			line = c.Pos.Line
		}
		got = append(got, fmt.Sprintf("%d:%t:%s", line, c.Free, c.Text))
	}
	want := []string{
		"1:false:// Package comments is the introspection subject for the",
		"2:false:// comment-enumeration tests: doc comments, free comments,",
		"3:false:// directive-shaped comments, and block comments.",
		"6:false:// Documented is a documented function — its doc comment is attached.",
		"7:true:// a trailing comment on the signature line",
		"8:true:// swagger:route GET /x — a free comment a doc scan cannot reach",
		"9:true:// a trailing comment inside a body is free too",
		"11:true://go:generate echo directive — directive-shaped text stays visible",
		"15:true:// a floating comment between decls — no declaration claims it",
		"17:false:// Bye is a second documented function.",
		"20:true:/* an inline block comment */",
		"22:false:// WithParams exercises comments inside a signature's param and",
		"23:false:// result lists — the parser leaves them unclaimed.",
		"25:true:// x is value of x",
		"26:true:/* y is */",
		"27:true:/* result comment */",
		"31:true:/* x is */",
		"31:true:/* y is ... */",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("comments (-want +got):\n%s", diff)
	}
}

// TestASTOf: the raw AST anchor behind a decl — the retained
// *ast.FuncDecl for funcs and methods, the decl's own spec for type,
// var, and const decls. Host symbols report an error.
func TestASTOf(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		decl *index.Decl
		want string // the anchor node's Go type
	}{
		{"Hello", p.Index.Funcs["Hello"], "*ast.FuncDecl"},
		{"Greet", p.Index.Types["User"].Methods["Greet"], "*ast.FuncDecl"},
		{"User", p.Index.Types["User"].Decl, "*ast.TypeSpec"},
		{"Count", p.Index.Vars["Count"], "*ast.ValueSpec"},
		{"Label", p.Index.Consts["Label"], "*ast.ValueSpec"},
	}
	for _, c := range cases {
		if c.decl == nil {
			t.Fatalf("no decl for %s", c.name)
		}
		node, err := xinspect.ASTOf(xinspect.NewDecl(p, c.decl))
		if err != nil {
			t.Fatalf("ASTOf(%s): %v", c.name, err)
		}
		if got := reflect.TypeOf(node).String(); got != c.want {
			t.Errorf("ASTOf(%s) = %s, want %s", c.name, got, c.want)
		}
	}
	// the func anchor keeps the body; the spec anchor keeps the name
	if fd, _ := xinspect.ASTOf(xinspect.NewDecl(p, p.Index.Funcs["Hello"])); fd.(*ast.FuncDecl).Body == nil {
		t.Error("Hello anchor lost its body")
	}
	if vs, _ := xinspect.ASTOf(xinspect.NewDecl(p, p.Index.Consts["Label"])); vs.(*ast.ValueSpec).Names[0].Name != "Label" {
		t.Error("Label anchor lost its spec")
	}
	if _, err := xinspect.ASTOf(xinspect.NewDecl(p, nil)); err == nil {
		t.Error("want error for a host symbol")
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

// TestBodyNodes: BodyOf roots a Node walk whose NodesOf enumeration
// visits exactly ast.Inspect's preorder — every child node, in the
// same order — so script-side body traversal loses nothing to the
// view layer. Kind/Role/Detail/Text carry the discrimination data.
func TestBodyNodes(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	d := p.Index.Funcs["WalkMe"]
	if d == nil {
		t.Fatal("no WalkMe decl")
	}
	root, err := xinspect.BodyOf(xinspect.NewDecl(p, d))
	if err != nil {
		t.Fatalf("BodyOf: %v", err)
	}
	if root.Kind != "BlockStmt" || root.Role != "" {
		t.Fatalf("root = %s/%q", root.Kind, root.Role)
	}

	var want []ast.Node
	ast.Inspect(d.Func.Body, func(n ast.Node) bool {
		if n != nil {
			want = append(want, n)
		}
		return true
	})
	var got []ast.Node
	var walk func(n *xinspect.Node)
	walk = func(n *xinspect.Node) {
		got = append(got, n.ASTNode())
		for _, c := range xinspect.NodesOf(n) {
			walk(c)
		}
	}
	walk(root)
	if len(got) != len(want) {
		t.Fatalf("walked %d nodes, want %d (ast.Inspect)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d: got %T %v, want %T %v", i, got[i], got[i], want[i], want[i])
		}
	}

	// the roles a registerRoutes-shaped walk navigates by
	stmts := xinspect.NodesOf(root)
	call := xinspect.NodesOf(stmts[0])[0] // ExprStmt -> CallExpr
	if call.Kind != "CallExpr" || call.Role != "X" {
		t.Fatalf("call = %s/%q", call.Kind, call.Role)
	}
	cs := xinspect.NodesOf(call)
	if len(cs) != 2 || cs[0].Role != "Fun" || cs[1].Role != "Args" {
		t.Fatalf("call children roles: %v", rolesOf(cs))
	}
	lit := cs[1]
	if lit.Kind != "BasicLit" || lit.Detail != "STRING" || lit.Text != `"a"` {
		t.Errorf("lit = %s/%s %q", lit.Kind, lit.Detail, lit.Text)
	}
	if lit.Pos == nil || lit.Pos.Line <= 0 {
		t.Errorf("lit.Pos = %v", lit.Pos)
	}
	// Expr anchors a body expression for the SymbolID machinery.
	fun := xinspect.ExprOf(cs[0])
	if fun == nil || fun.Kind != "SelectorExpr" {
		t.Fatalf("ExprOf(sel) = %v", fun)
	}
	if xinspect.ExprOf(root) != nil {
		t.Error("ExprOf on a statement should be nil")
	}

	// nil and non-func cases
	if b, err := xinspect.BodyOf(xinspect.NewDecl(p, p.Index.Funcs["NoBody"])); err != nil || b != nil {
		t.Errorf("NoBody: got %v, %v — want nil, nil", b, err)
	}
	if _, err := xinspect.BodyOf(xinspect.NewDecl(p, nil)); err == nil {
		t.Error("want error for a host symbol")
	}
	if _, err := xinspect.BodyOf(xinspect.NewDecl(p, p.Index.Vars["Count"])); err == nil {
		t.Error("want error for a var decl")
	}
}

func rolesOf(ns []*xinspect.Node) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Kind+":"+n.Role)
	}
	return out
}

// A name the func binds itself (param, :=, ...) is "possibly local",
// so Expr-fed SymbolID refuses to attribute `strings.TrimSpace` to
// the imported package when `strings` is a param — under-resolution,
// never misattribution.
func TestBodyScope(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	d := p.Index.Funcs["ShadowMe"]
	if d == nil {
		t.Fatal("no ShadowMe decl")
	}
	root, err := xinspect.BodyOf(xinspect.NewDecl(p, d))
	if err != nil {
		t.Fatalf("BodyOf: %v", err)
	}
	ret := xinspect.NodesOf(root)[0] // ReturnStmt
	call := xinspect.NodesOf(ret)[0] // Results -> CallExpr
	fun := xinspect.NodesOf(call)[0] // Fun -> SelectorExpr
	te := xinspect.ExprOf(fun)
	if te == nil {
		t.Fatal("ExprOf(sel) = nil")
	}
	if sid, ok := te.SymbolID(); ok {
		t.Errorf("shadowed select resolved to %v", sid)
	}
	x := xinspect.ExprOf(xinspect.NodesOf(fun)[0]) // X -> Ident strings
	if sid, ok := x.SymbolID(); ok {
		t.Errorf("shadowed ident resolved to %v", sid)
	}

	// control: WalkMe's unshadowed strings.TrimSpace still resolves —
	// it is the arg of helper(strings.TrimSpace("c")) at stmt 2.
	wd := p.Index.Funcs["WalkMe"]
	wroot, err := xinspect.BodyOf(xinspect.NewDecl(p, wd))
	if err != nil {
		t.Fatalf("BodyOf WalkMe: %v", err)
	}
	wcall := xinspect.NodesOf(xinspect.NodesOf(wroot)[2])[0] // helper(...)
	inner := xinspect.NodesOf(wcall)[1]                      // Args -> CallExpr
	wfun := xinspect.ExprOf(xinspect.NodesOf(inner)[0])      // Fun -> SelectorExpr
	sid, ok := wfun.SymbolID()
	if !ok || sid.PackagePath != "strings" || sid.Name != "TrimSpace" {
		t.Errorf("unshadowed select = %v, %v", sid, ok)
	}
}

// TestTypeFieldsError pins the contract the script-visible TypeFields
// intrinsic shares: a non-composite TypeExpr reports an error (a trap
// in scripts), so a walk gates on Kind first instead of reading an
// empty field list as "no members". Doc strings arrive trimmed of the
// trailing newline CommentGroup.Text appends.
func TestTypeFieldsError(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	d := lookupType(t, p, "User")
	if strings.HasSuffix(d.Doc, "\n") || d.Doc == "" {
		t.Errorf("decl doc not trimmed: %q", d.Doc)
	}
	fs, err := xinspect.FieldsOf(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(fs[0].Doc, "\n") || fs[0].Doc == "" {
		t.Errorf("field doc not trimmed: %q", fs[0].Doc)
	}
	if _, err := xinspect.TypeFieldsOf(fs[0].Type); err == nil {
		t.Error("TypeFieldsOf on a named leaf: want error")
	}
	te, err := xinspect.DefOf(d)
	if err != nil {
		t.Fatal(err)
	}
	sfs, err := xinspect.TypeFieldsOf(te)
	if err != nil || len(sfs) != len(fs) {
		t.Errorf("TypeFieldsOf on the struct spelling: %v %d", err, len(sfs))
	}
}

// TestTypeParamSymbolID pins the marker SymbolID carries when an
// identifier is bound by the enclosing generic decl — the :typeparam:
// pseudo path answers "is this a type parameter?" directly, where the
// pre-marker walk attributed T to the declaring package and a resolver
// probing it trapped "inspect.Symbol: no T".
func TestTypeParamSymbolID(t *testing.T) {
	e := newEngine(t)
	ctx := context.Background()
	p, err := e.Package(ctx, "./testdata/inspectpkg")
	if err != nil {
		t.Fatal(err)
	}
	// a generic struct's field spelled T is marked.
	fs := fieldTypes(t, lookupType(t, p, "Pair"))
	ta := fs["T"]
	if sid, ok := ta.SymbolID(); !ok || sid.PackagePath != xinspect.TypeParamPackagePath || sid.Name != "T" {
		t.Errorf("Pair.A SymbolID = %v, %v — want {:typeparam:, T}", sid, ok)
	}
	// the marked identifier never merges into the package namespace:
	// Unwrap stops on it like it does on builtins.
	if sid, _ := ta.Unwrap(nil).SymbolID(); sid.PackagePath != xinspect.TypeParamPackagePath {
		t.Errorf("Unwrap on a type parameter changed the mark: %v", sid)
	}
	// a two-parameter decl marks each name.
	fs2 := fieldTypes(t, lookupType(t, p, "Two"))
	if sid, _ := fs2["K"].SymbolID(); sid.PackagePath != xinspect.TypeParamPackagePath {
		t.Errorf("Two.K = %v", sid)
	}
	if sid, _ := fs2["V"].SymbolID(); sid.PackagePath != xinspect.TypeParamPackagePath {
		t.Errorf("Two.V = %v", sid)
	}
	// a generic alias's argument position marks the parameter.
	d := lookupType(t, p, "APair")
	def, err := xinspect.DefOf(d)
	if err != nil {
		t.Fatal(err)
	}
	if sid, _ := def.Children()[1].SymbolID(); sid.PackagePath != xinspect.TypeParamPackagePath {
		t.Errorf("APair arg SymbolID = %v", sid)
	}
	// a non-generic alias to an instantiation is untouched: the base
	// resolves to the package's Pair decl.
	d = lookupType(t, p, "IPair")
	def, err = xinspect.DefOf(d)
	if err != nil {
		t.Fatal(err)
	}
	if sid, _ := def.Children()[0].SymbolID(); sid.PackagePath != p.Path || sid.Name != "Pair" {
		t.Errorf("IPair def base SymbolID = %v", sid)
	}
	// a method binds its receiver's parameter names.
	ms, err := xinspect.MethodsOf(lookupType(t, p, "Pair"))
	if err != nil {
		t.Fatal(err)
	}
	var head *xinspect.Decl
	for _, m := range ms {
		if m.Name == "Head" {
			head = m
		}
	}
	if head == nil {
		t.Fatal("no Head method")
	}
	sig, err := xinspect.SignatureOf(head)
	if err != nil {
		t.Fatal(err)
	}
	if sid, _ := sig.ResultFields()[0].Type.SymbolID(); sid.PackagePath != xinspect.TypeParamPackagePath {
		t.Errorf("Head result SymbolID = %v", sid)
	}
	// ordinary package types keep resolving to their package — the
	// instantiation base is unaffected by the marker.
	fsi := fieldTypes(t, lookupType(t, p, "Inst"))
	if sid, ok := fsi["Pair[int]"].Children()[0].SymbolID(); !ok || sid.PackagePath != p.Path || sid.Name != "Pair" {
		t.Errorf("Inst.P base SymbolID = %v, %v", sid, ok)
	}
}

// TestLitKeySymbolIDs: a composite-literal key reports a SymbolID only
// when the literal's type resolves to a map or array — through generic
// instantiations, elided inner literals, local and package-level named
// types and aliases. Struct keys (fields named KeyA, like the const)
// never do: under-resolution is allowed, misattribution is not.
func TestLitKeySymbolIDs(t *testing.T) {
	e := newEngine(t)
	p, err := e.Package(context.Background(), "./testdata/litkeys")
	if err != nil {
		t.Fatal(err)
	}
	pkg := p.Path
	want := map[string][]string{
		"GenericMap":        {"KeyA=" + pkg + ".KeyA"},
		"ElidedInner":       {"KeyA=" + pkg + ".KeyA", "KeyA=" + pkg + ".KeyA", "KeyB=" + pkg + ".KeyB", "KeyA=" + pkg + ".KeyA", "KeyA=" + pkg + ".KeyA", "Idx=" + pkg + ".Idx"},
		"LocalNamed":        {"KeyA=" + pkg + ".KeyA", "KeyA=" + pkg + ".KeyA", "KeyA=" + pkg + ".KeyA"},
		"Aliases":           {"KeyA=" + pkg + ".KeyA", "KeyA=" + pkg + ".KeyA"},
		"Imported":          {"KeyA=nil"},
		"GenericElem":       {"KeyA=" + pkg + ".KeyA", "KeyB=nil"},
		"Structs":           {"KeyA=nil", "KeyA=nil", "KeyA=nil", "KeyB=" + pkg + ".KeyB", "KeyA=nil", "KeyA=nil", "KeyA=nil", "KeyA=nil"},
		"LocalShadow":       {"KeyA=nil"},
		"SiblingScopes":     {"KeyA=" + pkg + ".KeyA", "KeyA=nil"},
		"LocalElidedStruct": {"KeyA=nil"},
	}
	got := map[string][]string{}
	for name := range want {
		d := p.Index.Funcs[name]
		if d == nil {
			t.Fatalf("no func %s", name)
		}
		root, err := xinspect.BodyOf(xinspect.NewDecl(p, d))
		if err != nil {
			t.Fatalf("BodyOf(%s): %v", name, err)
		}
		var walk func(n *xinspect.Node)
		walk = func(n *xinspect.Node) {
			for _, c := range xinspect.NodesOf(n) {
				if n.Kind == "KeyValueExpr" && c.Kind == "Ident" && c.Role == "Key" {
					s := "nil"
					if sid, ok := xinspect.ExprOf(c).SymbolID(); ok {
						s = sid.PackagePath + "." + sid.Name
					}
					got[name] = append(got[name], c.Text+"="+s)
				}
				walk(c)
			}
		}
		walk(root)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("key SymbolIDs mismatch (-want +got):\n%s", diff)
	}
}
