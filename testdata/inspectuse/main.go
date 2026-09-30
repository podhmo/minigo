package main

import (
	"strings"

	"github.com/podhmo/minigo/inspect"
	subj "github.com/podhmo/minigo/testdata/inspectpkg"
)

// PkgAccess: locators load a package, metadata functions read it.
func PkgAccess() string {
	p := inspect.PackageOf("strings")
	if inspect.Path(p) != "strings" || inspect.Name(p) != "strings" {
		return "bad meta"
	}
	if !inspect.Standard(p) {
		return "not standard"
	}
	d := inspect.DirOf("./testdata/inspectpkg")
	if inspect.Name(d) != "inspectpkg" {
		return "bad dir: " + inspect.Name(d)
	}
	f := inspect.FileOf("./testdata/inspectpkg/main.go")
	if len(inspect.Decls(f)) == 0 {
		return "no decls"
	}
	return "ok"
}

// DeclsList: package-level enumeration.
func DeclsList() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	var kinds string
	found := 0
	for _, s := range inspect.Decls(p) {
		kinds += s.Kind + ","
		if s.Name == "User" {
			found++
		}
	}
	if found != 1 {
		return "User not found in " + kinds
	}
	if !strings.Contains(kinds, "func") || !strings.Contains(kinds, "type") ||
		!strings.Contains(kinds, "var") || !strings.Contains(kinds, "const") {
		return "missing kind in " + kinds
	}
	return "ok"
}

// SymbolView: Kind/Doc/Pos on a decl.
func SymbolView() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	s := inspect.Symbol(p, "Hello")
	if s.Kind != "func" {
		return "bad kind: " + s.Kind
	}
	if !strings.Contains(s.Doc, "plain function") {
		return "bad doc: " + s.Doc
	}
	if !strings.Contains(s.Pos, "main.go:27:") {
		return "bad pos: " + s.Pos
	}
	u := inspect.Symbol(p, "User")
	if !strings.Contains(u.Doc, "struct type to walk") {
		return "bad type doc"
	}
	return "ok"
}

// FieldsWalk: struct field iteration.
func FieldsWalk() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	u := inspect.Symbol(p, "User")
	fs := inspect.Fields(u)
	if len(fs) != 4 {
		return "want 4 fields"
	}
	f0 := fs[0]
	if f0.Names[0] != "Name" || f0.Type.Text != "string" {
		return "bad field0"
	}
	if !strings.Contains(f0.Tag, "json") {
		return "bad tag: " + f0.Tag
	}
	if f0.Embedded {
		return "not embedded"
	}
	f2 := fs[2]
	if !f2.Embedded || f2.Type.Text != "*Base" {
		return "bad embedded: " + f2.Type.Text
	}
	f3 := fs[3]
	if f3.Type.Kind != "SelectorExpr" {
		return "bad kind: " + f3.Type.Kind
	}
	return "ok"
}

// MethodsWalk: method decl views.
func MethodsWalk() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	u := inspect.Symbol(p, "User")
	n := 0
	names := ""
	for _, m := range inspect.Methods(u) {
		if m.Kind != "method" {
			return "bad method kind: " + m.Kind
		}
		n++
		names += m.Name + ","
	}
	if n != 2 {
		return "want 2 methods, got " + names
	}
	return "ok"
}

// SignatureWalk: signature views on func and method.
func SignatureWalk() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	s := inspect.Symbol(p, "Hello")
	sig := inspect.Signature(s)
	if sig.Recv != nil {
		return "unexpected recv"
	}
	if len(sig.Params) != 1 || len(sig.Results) != 1 {
		return "bad arity"
	}
	m := inspect.Symbol(p, "Hello")
	for _, mm := range inspect.Methods(inspect.Symbol(p, "User")) {
		if mm.Name == "Greet" {
			m = mm
		}
	}
	if m.Kind != "method" {
		return "Greet is not method"
	}
	msig := inspect.Signature(m)
	if msig.Recv == nil || msig.Recv.Type.Text != "*User" {
		return "bad recv"
	}
	return "ok"
}

// TypeExprNav: Children/UnRef/SymbolID/Resolve/SameType.
func TypeExprNav() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	u := inspect.Symbol(p, "User")
	fs := inspect.Fields(u)
	// embedded *Base -> Unref -> Ident Base -> SymbolID
	base := inspect.UnRef(fs[2].Type)
	if base.Text != "Base" {
		return "bad unref: " + base.Text
	}
	sid := inspect.SymbolID(base)
	if sid == nil || !strings.HasSuffix(sid.PackagePath, "inspectpkg") || sid.Name != "Base" {
		return "bad sid"
	}
	// selector type -> SymbolID -> Resolve crosses packages
	sid2 := inspect.SymbolID(fs[3].Type)
	if sid2.PackagePath != "strings" || sid2.Name != "Builder" {
		return "bad sel sid"
	}
	// builtin ident
	sid3 := inspect.SymbolID(fs[0].Type)
	if sid3.PackagePath != ":builtin:" || sid3.Name != "string" {
		return "bad builtin sid"
	}
	// slice children
	sl := inspect.Symbol(p, "StrList")
	def := inspect.Def(sl)
	kids := inspect.Children(def)
	if len(kids) != 1 || kids[0].Text != "string" {
		return "bad children"
	}
	// SameType: identical spelling, different decl -> false; same decl -> true
	if inspect.SameType(fs[0].Type, fs[0].Type) != true {
		return "SameType self"
	}
	b := inspect.Symbol(p, "Base")
	if inspect.SameType(fs[1].Type, inspect.Def(b)) {
		return "SameType int vs Base"
	}
	return "ok"
}

// OriginNav: Origin chases pointers + declared transitions.
func OriginNav() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	pi := inspect.Symbol(p, "PInt")
	o := inspect.Origin(inspect.Def(pi))
	if o.Text != "int" {
		return "bad origin: " + o.Text
	}
	mi := inspect.Symbol(p, "MyInt")
	if inspect.SameType(o, inspect.Def(mi)) != true {
		return "origin not int"
	}
	return "ok"
}

// OwnerChain: symbol -> package across value kinds.
func OwnerChain() string {
	if inspect.PathOf(strings.Contains) != "strings" {
		return "bad PathOf"
	}
	p2 := inspect.OwnerOf(strings.Contains)
	if p2 == nil || inspect.Path(p2) != "strings" {
		return "bad OwnerOf"
	}
	s := inspect.SymbolOf(strings.Contains)
	if s.Kind != "host" || s.Name != "Contains" {
		return "bad host sym: " + s.Kind + "/" + s.Name
	}
	hs := inspect.SymbolOf(subj.Hello)
	if hs.Kind != "func" || inspect.Path(hs.Package) != inspect.Path(subj) {
		return "bad func sym"
	}
	ms := inspect.SymbolOf(subj.Hello)
	if ms.Package != hs.Package {
		return "pkg mismatch"
	}
	// type + struct instance
	ts := inspect.SymbolOf(subj.User{})
	if ts.Kind != "type" || ts.Name != "User" {
		return "bad struct sym: " + ts.Name
	}
	if inspect.PathOf(subj.User{}) != inspect.Path(subj) {
		return "bad inst owner"
	}
	// an intrinsic bound under two paths keeps its first owner
	if inspect.PathOf(inspect.PackageOf) != "minigo.dev/inspect" {
		return "bad alias owner: " + inspect.PathOf(inspect.PackageOf)
	}
	return "ok"
}

// HostSignature: Signature on a bound intrinsic via BuiltinFunc.Target.
func HostSignature() string {
	s := inspect.SymbolOf(strings.Contains)
	sig := inspect.Signature(s)
	if sig == nil || len(sig.Params) != 2 || len(sig.Results) != 1 {
		return "bad host sig"
	}
	if sig.Params[0].Type.Text != "string" || sig.Results[0].Type.Text != "bool" {
		return "bad host sig text"
	}
	return "ok"
}

// CurrentPkg: the caller's own package.
func CurrentPkg() string {
	p := inspect.Current()
	if p == nil || !strings.Contains(inspect.Path(p), "inspectuse") {
		return "bad current"
	}
	return "ok"
}

// ImportsList: a file's import table.
func ImportsList() string {
	f := inspect.FileOf("./testdata/inspectpkg/main.go")
	imps := inspect.Imports(f)
	if len(imps) != 1 || imps[0].Path != "strings" {
		return "bad imports"
	}
	used := inspect.UsedSymbols(f)
	found := false
	for _, sid := range used {
		if sid.PackagePath == "strings" && sid.Name == "Builder" {
			found = true
		}
	}
	if !found {
		return "missing used symbol"
	}
	return "ok"
}

// ValueLayer: Value/TypeOf materialize.
func ValueLayer() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	fn := inspect.Value(p, "Hello")
	if fn == nil {
		return "nil value"
	}
	got := fn("x")
	if got != "hello x" {
		return "bad call: "
	}
	u := inspect.Symbol(p, "User")
	td := inspect.TypeOf(u)
	if td == nil {
		return "nil typedef"
	}
	return "ok"
}

// RecursiveOrigin: type Node *Node terminates instead of looping.
func RecursiveOrigin() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	n := inspect.Symbol(p, "Node")
	o := inspect.Origin(inspect.Def(n))
	if o.Text != "Node" {
		return "bad recursive origin: " + o.Text
	}
	return "ok"
}

// ShapeDetails: [2]int and [3]int are not the same type; [1+1]int is.
func ShapeDetails() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	a2 := inspect.Symbol(p, "Arr2")
	a3 := inspect.Symbol(p, "Arr3")
	if inspect.SameType(inspect.Def(a2), inspect.Def(a3)) {
		return "array lengths confused"
	}
	if !inspect.SameType(inspect.Def(a2), inspect.Def(a2)) {
		return "self not equal"
	}
	ae := inspect.Symbol(p, "ArrExpr")
	if !inspect.SameType(inspect.Def(a2), inspect.Def(ae)) {
		return "[2]int != [1+1]int"
	}
	an := inspect.Symbol(p, "ArrN")
	if !inspect.SameType(inspect.Def(a2), inspect.Def(an)) {
		return "[2]int != [sizeN]int"
	}
	am := inspect.Symbol(p, "ArrM")
	if inspect.SameType(inspect.Def(an), inspect.Def(am)) {
		return "[sizeN]int == [sizeM]int"
	}
	return "ok"
}

// MethodValueSym: SymbolOf on a bound method finds the method decl.
func MethodValueSym() string {
	u := subj.User{Name: "x"}
	s := inspect.SymbolOf(u.Greet)
	if s == nil || s.Kind != "method" || s.Name != "Greet" {
		return "method value not resolved"
	}
	if inspect.PathOf(u.Greet) != inspect.Path(subj) {
		return "bad method owner"
	}
	return "ok"
}

// HostPtrOwner: OwnerOf on a host pointer finds the element package.
func HostPtrOwner() string {
	r := strings.NewReader("x")
	p := inspect.OwnerOf(r)
	if p == nil || inspect.Path(p) != "strings" {
		return "bad host ptr owner"
	}
	return "ok"
}

// TypeOfGuards: TypeOf on a func decl traps instead of returning a func
// (the trap assertion lives in inspect_test.go since scripts cannot
// catch intrinsic errors).
func TypeOfFuncTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	s := inspect.Symbol(p, "Hello")
	td := inspect.TypeOf(s)
	if td != nil {
		return "expected trap"
	}
	return "swallowed"
}

// TypeOfType: TypeOf on a type decl yields the TypeDef.
func TypeOfType() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	s := inspect.Symbol(p, "User")
	if inspect.TypeOf(s) == nil {
		return "nil typedef"
	}
	return "ok"
}

// IfaceMembers: interface decls expose member views — Fields covers
// method specs and embedded elements; MReqs/IEmbeds split them.
func IfaceMembers() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	t := inspect.Symbol(p, "Talker")
	fs := inspect.Fields(t)
	if len(fs) != 2 {
		return "want 2 members"
	}
	if !fs[0].Embedded || fs[0].Type.Text != "Speaker" {
		return "bad embed member: " + fs[0].Type.Text
	}
	if fs[1].Embedded || fs[1].Names[0] != "Talk" || fs[1].Type.Kind != "FuncType" {
		return "bad method member"
	}
	// MReqs: named method specs only
	reqs := inspect.MReqs(t)
	if len(reqs) != 1 || reqs[0].Names[0] != "Talk" {
		return "bad mreqs"
	}
	// a method spec's FuncType children are its param then result types
	kids := inspect.Children(reqs[0].Type)
	if len(kids) != 2 || kids[0].Text != "string" || kids[1].Text != "error" {
		return "bad sig children"
	}
	// IEmbeds: the embedded interface name, resolvable to its decl
	emb := inspect.IEmbeds(t)
	if len(emb) != 1 || emb[0].Text != "Speaker" {
		return "bad iembeds"
	}
	sid := inspect.SymbolID(emb[0])
	if sid == nil || sid.Name != "Speaker" || !strings.HasSuffix(sid.PackagePath, "inspectpkg") {
		return "bad embed sid"
	}
	d := inspect.Resolve(emb[0])
	if d == nil || d.Name != "Speaker" || d.Kind != "type" {
		return "bad embed resolve"
	}
	// a constraint interface has no named requirements; its union is
	// one embedded element with the ~terms as children
	n := inspect.Symbol(p, "Number")
	if len(inspect.MReqs(n)) != 0 {
		return "mreqs on constraint"
	}
	ne := inspect.IEmbeds(n)
	if len(ne) != 1 || ne[0].Kind != "BinaryExpr" {
		return "bad union embed"
	}
	elts := inspect.Children(ne[0])
	if len(elts) != 2 || elts[0].Text != "~int" || elts[1].Text != "~int64" {
		return "bad union children"
	}
	return "ok"
}

// MReqsStructTrap: MReqs on a non-interface decl traps (asserted
// Go-side — scripts cannot catch intrinsic errors).
func MReqsStructTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	s := inspect.Symbol(p, "User")
	if inspect.MReqs(s) != nil {
		return "expected trap"
	}
	return "swallowed"
}

// SourceOfSrc: a bound-shadowed path still yields its GOROOT source —
// real decls where PackageOf returns host pseudo-decls.
func SourceOfSrc() string {
	src := inspect.SourceOf("strings")
	if src == nil {
		return "nil source pkg"
	}
	if inspect.Path(src) != "strings" || !inspect.Standard(src) {
		return "bad source meta"
	}
	// the source package indexes real decls with names and signatures
	d := inspect.Symbol(src, "Contains")
	if d == nil || d.Kind != "func" {
		return "bad source decl kind"
	}
	sig := inspect.Signature(d)
	if sig == nil || len(sig.Params) != 1 || len(sig.Results) != 1 {
		return "bad source sig"
	}
	// `func Contains(s, substr string) bool` — one param field, two names
	if sig.Params[0].Names[0] != "s" || sig.Params[0].Names[1] != "substr" {
		return "source sig lost param names"
	}
	// the bound shadow still answers the canonical lookup
	bound := inspect.PackageOf("strings")
	if bound == src {
		return "SourceOf leaked into pkgs"
	}
	if inspect.Symbol(bound, "Contains").Kind != "host" {
		return "bound shadow broken"
	}
	// an unbound path returns the canonical package itself
	p := inspect.SourceOf("github.com/podhmo/minigo/testdata/inspectpkg")
	if p != inspect.PackageOf("github.com/podhmo/minigo/testdata/inspectpkg") {
		return "unbound SourceOf should be canonical"
	}
	return "ok"
}

// BoundDecls: a bound package enumerates as host pseudo-symbols.
func BoundDecls() string {
	p := inspect.PackageOf("strings")
	n := 0
	for _, s := range inspect.Decls(p) {
		if s.Kind != "host" {
			return "non-host kind: " + s.Kind
		}
		n++
	}
	if n < 10 {
		return "too few bound decls"
	}
	return "ok"
}

func main() {}
