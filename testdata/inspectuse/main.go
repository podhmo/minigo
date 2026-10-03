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
	if !strings.Contains(s.Pos, "main.go:30:") {
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
	if len(imps) != 2 || imps[0].Path != "strings" || imps[1].Path != "time" {
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

// DeclMeta: File/Pos/Doc on every decl kind, including unexported
// names that only the index sees.
func DeclMeta() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	names := []string{"Hello", "User", "MyInt", "PInt", "AInt", "StrList",
		"Count", "Label", "Talker", "hidden", "hiddenVar"}
	for _, name := range names {
		s := inspect.Symbol(p, name)
		if s == nil {
			return "missing " + name
		}
		if !strings.HasSuffix(s.File, "inspectpkg/main.go") {
			return name + " bad file: " + s.File
		}
		if !strings.Contains(inspect.Pos(s), "main.go:") {
			return name + " bad pos: " + inspect.Pos(s)
		}
	}
	for _, name := range []string{"Hello", "User", "MyInt", "Count", "Label"} {
		if inspect.Symbol(p, name).Doc == "" {
			return name + " missing doc"
		}
	}
	// method decls carry the same metadata as top-level decls
	for _, m := range inspect.Methods(inspect.Symbol(p, "User")) {
		if !strings.HasSuffix(m.File, "main.go") ||
			!strings.Contains(inspect.Pos(m), "main.go:") || m.Doc == "" {
			return "bad method meta: " + m.Name
		}
	}
	return "ok"
}

// FieldPos: per-field Pos, plus param-level names and Pos on sigs.
func FieldPos() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	for _, f := range inspect.Fields(inspect.Symbol(p, "User")) {
		if !strings.Contains(f.Pos, "main.go:") {
			return "bad field pos: " + f.Pos
		}
	}
	sig := inspect.Signature(inspect.Symbol(p, "Hello"))
	if sig.Params[0].Names[0] != "s" ||
		!strings.Contains(sig.Params[0].Pos, "main.go:") {
		return "bad param meta"
	}
	return "ok"
}

// PkgMeta: Files/Dir/State plus the File view's own fields.
// NOTE: must run before VarValueRead — a bare DirOf load stops at
// "indexed"; Value triggers package init and flips it to "ready".
func PkgMeta() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	fsl := inspect.Files(p)
	if len(fsl) != 1 {
		return "bad files"
	}
	if !strings.HasSuffix(fsl[0].Name, "inspectpkg/main.go") {
		return "bad file name: " + fsl[0].Name
	}
	if !strings.HasSuffix(inspect.Dir(p), "testdata/inspectpkg") {
		return "bad dir: " + inspect.Dir(p)
	}
	return "ok"
}

// CompositeFields: map/chan/func/interface field type expressions.
func CompositeFields() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	fs := inspect.Fields(inspect.Symbol(p, "Rec"))
	if len(fs) != 6 {
		return "want 6 fields"
	}
	if fs[0].Type.Kind != "MapType" || fs[1].Type.Kind != "ChanType" ||
		fs[2].Type.Kind != "FuncType" || fs[3].Type.Kind != "Ident" {
		return "bad composite kinds"
	}
	kids := inspect.Children(fs[0].Type)
	if len(kids) != 2 || kids[0].Text != "string" || kids[1].Text != "int" {
		return "bad map children"
	}
	fk := inspect.Children(fs[2].Type)
	if len(fk) != 2 || fk[0].Text != "int" || fk[1].Text != "bool" {
		return "bad func children"
	}
	sid := inspect.SymbolID(fs[3].Type)
	if sid == nil || sid.Name != "Speaker" ||
		!strings.HasSuffix(sid.PackagePath, "inspectpkg") {
		return "bad iface field sid"
	}
	return "ok"
}

// NamedFieldType: a named-type field unwraps to its underlying expr;
// UnWrap on a non-named shape is identity.
func NamedFieldType() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	fs := inspect.Fields(inspect.Symbol(p, "Rec"))
	u := inspect.UnWrap(fs[4].Type)
	if u.Text != "int" {
		return "bad named unwrap: " + u.Text
	}
	s := inspect.UnWrap(inspect.Def(inspect.Symbol(p, "User")))
	if s == nil || s.Kind != "StructType" {
		return "bad struct unwrap"
	}
	return "ok"
}

// Instantiation: a Pair[int] field reads as IndexExpr — the generic
// origin is NOT reachable (SymbolID -> nil, Children yields args).
func Instantiation() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	fs := inspect.Fields(inspect.Symbol(p, "Rec"))
	ip := fs[5].Type
	if ip.Kind != "IndexExpr" || ip.Text != "Pair[int]" {
		return "bad inst: " + ip.Text + "/" + ip.Kind
	}
	kids := inspect.Children(ip)
	if len(kids) != 1 || kids[0].Text != "int" {
		return "bad inst children"
	}
	if inspect.SymbolID(ip) != nil {
		return "unexpected inst sid"
	}
	return "ok"
}

// TypeParamsList: generic type and generic func expose their params;
// a named constraint resolves to its interface decl.
func TypeParamsList() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	tps := inspect.TypeParams(inspect.Symbol(p, "Pair"))
	if len(tps) != 1 || tps[0].Names[0] != "T" || tps[0].Type.Text != "any" {
		return "bad type tparams"
	}
	fps := inspect.TypeParams(inspect.Symbol(p, "Reduce"))
	if len(fps) != 1 || fps[0].Names[0] != "T" || fps[0].Type.Text != "Number" {
		return "bad func tparams"
	}
	sid := inspect.SymbolID(fps[0].Type)
	if sid == nil || sid.Name != "Number" ||
		!strings.HasSuffix(sid.PackagePath, "inspectpkg") {
		return "bad constraint sid"
	}
	return "ok"
}

// TypeOfNamed: TypeOf materializes typedefs for non-struct types too.
func TypeOfNamed() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	for _, n := range []string{"MyInt", "PInt", "StrList", "Talker", "Pair"} {
		if inspect.TypeOf(inspect.Symbol(p, n)) == nil {
			return "nil typedef: " + n
		}
	}
	return "ok"
}

// BoundTypeSym: bound non-func symbols enumerate as host decls with
// the package attached — but no File/Pos/Doc (host kind).
func BoundTypeSym() string {
	bp := inspect.PackageOf("strings")
	b := inspect.Symbol(bp, "Builder")
	if b == nil || b.Kind != "host" || b.Name != "Builder" {
		return "bad bound type"
	}
	if b.Package == nil || inspect.Path(b.Package) != "strings" {
		return "bad bound pkg"
	}
	return "ok"
}

// HostMethodSym: a reflective host method value yields a host decl
// whose owner, signature, and position are recovered through the
// declared method's Func — the bound method value's own PC is a
// reflect thunk (reflect.methodValueCall) that locates nothing.
func HostMethodSym() string {
	r := strings.NewReader("x")
	s := inspect.SymbolOf(r.Size)
	if s == nil || s.Kind != "host" || s.Name != "Size" {
		return "bad host method sym"
	}
	if s.Package == nil || inspect.Path(s.Package) != "strings" {
		return "bad host method owner"
	}
	if inspect.PathOf(r.Size) != "strings" {
		return "bad host method path"
	}
	if !strings.Contains(inspect.Pos(s), "reader.go:") {
		return "bad host method pos: " + inspect.Pos(s)
	}
	sig := inspect.Signature(s)
	if sig == nil || sig.Recv == nil || sig.Recv.Type.Text != "*strings.Reader" {
		return "bad host method recv"
	}
	if len(sig.Params) != 0 || len(sig.Results) != 1 ||
		sig.Results[0].Type.Text != "int64" {
		return "bad host method sig"
	}
	// a parameterized method recovers its param types too
	s2 := inspect.SymbolOf(r.Seek)
	sig2 := inspect.Signature(s2)
	if sig2 == nil || len(sig2.Params) != 2 || len(sig2.Results) != 2 {
		return "bad host method params"
	}
	if sig2.Params[0].Type.Text != "int64" || sig2.Params[1].Type.Text != "int" {
		return "bad host method param types"
	}
	sid := inspect.SymbolIDOf(r.Size)
	if sid == nil || sid.Name != "Size" || sid.PackagePath != "strings" {
		return "bad host method sid"
	}
	return "ok"
}

// SourceOfStruct: through SourceOf a bound stdlib type exposes real
// fields, methods, positions, and named signatures.
func SourceOfStruct() string {
	src := inspect.SourceOf("strings")
	b := inspect.Symbol(src, "Builder")
	if b == nil || b.Kind != "type" {
		return "bad src builder"
	}
	if !strings.Contains(inspect.Pos(b), ".go:") {
		return "no src pos: " + inspect.Pos(b)
	}
	if inspect.State(src) != "indexed" {
		return "bad src state: " + inspect.State(src)
	}
	found := false
	for _, m := range inspect.Methods(b) {
		if m.Name == "WriteString" {
			found = true
			if !strings.Contains(inspect.Pos(m), ".go:") {
				return "no src method pos"
			}
			sig := inspect.Signature(m)
			if sig == nil || len(sig.Params) != 1 ||
				sig.Params[0].Names[0] != "s" {
				return "bad src method sig"
			}
		}
	}
	if !found {
		return "WriteString missing"
	}
	if len(inspect.Fields(b)) == 0 {
		return "no src fields"
	}
	return "ok"
}

// VarValueRead: Value materializes a package var (runs init on
// demand). Keep after PkgMeta — this flips State to "ready".
func VarValueRead() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	v := inspect.Value(p, "Count")
	if *v != 3 {
		return "bad var value"
	}
	return "ok"
}

// PkgMetaView: member access on a first-class *runtime.Package is
// namespace-only — a member named like a metadata field still wins
// (issue #26, option 2: keep pkg.X = member lookup uniform; metadata
// reads go through the inspect.* accessors).
// Runs init via p.Path — keep after PkgMeta/VarValueRead.
func PkgMetaView() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	// the var Path member shadows the Path field; the canonical
	// accessor still reports the import path
	if p.Path != "member-shadow" {
		return "member lost: " + p.Path
	}
	if inspect.Path(p) == p.Path || !strings.HasSuffix(inspect.Path(p), "inspectpkg") {
		return "bad canonical path: " + inspect.Path(p)
	}
	if inspect.Name(p) != "inspectpkg" {
		return "bad name: " + inspect.Name(p)
	}
	if !strings.HasSuffix(inspect.Dir(p), "testdata/inspectpkg") {
		return "bad dir: " + inspect.Dir(p)
	}
	if inspect.Standard(p) {
		return "std?"
	}
	if inspect.State(p) == "" {
		return "no state"
	}
	// the same resolution through a decl's owning package
	d := inspect.Symbol(p, "Hello")
	if d.Package.Path != "member-shadow" || inspect.Path(d.Package) != inspect.Path(p) {
		return "bad decl pkg view"
	}
	// the current package reads metadata through accessors too —
	// a file-loaded script gets the synthetic <file> path
	c := inspect.Current()
	if inspect.Path(c) == "" || inspect.Name(c) == "" {
		return "bad current meta"
	}
	// bound packages too
	b := inspect.PackageOf("strings")
	if inspect.Path(b) != "strings" || inspect.Name(b) != "strings" || !inspect.Standard(b) {
		return "bad bound meta"
	}
	return "ok"
}

// EnumWalk: enum members collect through EnumMembers, and DeclType
// reads a value spec's annotation — explicit or iota-inherited.
func EnumWalk() string {
	p := inspect.DirOf("./testdata/inspectpkg")

	st := inspect.Symbol(p, "Status")
	var names string
	for _, m := range inspect.EnumMembers(st) {
		if m.Kind != "const" {
			return "non-const member: " + m.Name
		}
		names += m.Name + ","
	}
	want := "StatusUnknown,StatusTodo,StatusDone,StatusExtra,FlagA,FlagB,"
	if names != want {
		return "bad Status members: " + names
	}

	names = ""
	for _, m := range inspect.EnumMembers(inspect.Symbol(p, "Priority")) {
		names += m.Name + ","
	}
	if names != "Low,High,PA,PB," {
		return "bad Priority members: " + names
	}
	// a named type with no matching constants is not an enum
	if len(inspect.EnumMembers(inspect.Symbol(p, "MyInt"))) != 0 {
		return "MyInt should not be an enum"
	}

	// DeclType: explicit annotations and iota inheritance.
	if got := inspect.DeclType(inspect.Symbol(p, "StatusUnknown")); got == nil || got.Text != "Status" {
		return "explicit type lost"
	}
	if got := inspect.DeclType(inspect.Symbol(p, "StatusTodo")); got == nil || got.Text != "Status" {
		return "inherited type lost"
	}
	// FlagD inherits the untyped spec above it — no type, no member.
	if got := inspect.DeclType(inspect.Symbol(p, "FlagD")); got != nil {
		return "FlagD should be untyped"
	}
	if got := inspect.DeclType(inspect.Symbol(p, "Loose")); got != nil {
		return "untyped const typed?"
	}
	if got := inspect.DeclType(inspect.Symbol(p, "CurrentStatus")); got == nil || got.Text != "Status" {
		return "var type lost"
	}
	fd := inspect.DeclType(inspect.Symbol(p, "ForDur"))
	if fd == nil || fd.Kind != "SelectorExpr" {
		return "foreign type lost"
	}
	// the linking primitive composes: member type -> SymbolID.
	sid := inspect.SymbolID(inspect.DeclType(inspect.Symbol(p, "StatusDone")))
	if sid == nil || sid.Name != "Status" || !strings.HasSuffix(sid.PackagePath, "inspectpkg") {
		return "bad member sid"
	}
	return "ok"
}

// AliasWalk: IsAlias partitions type decls into alias vs defined —
// the one axis Kind:"type" hides. Enum-ness is orthogonal.
func AliasWalk() string {
	p := inspect.DirOf("./testdata/inspectpkg")

	// alias forms: plain, foreign selector, grouped, generic.
	for _, name := range []string{"AInt", "Dur", "AFloat", "APair"} {
		if !inspect.IsAlias(inspect.Symbol(p, name)) {
			return name + " should be an alias"
		}
	}
	// defined forms: basic newtype, struct, interface, grouped,
	// pointer-underlying.
	for _, name := range []string{"MyInt", "User", "Speaker", "BFloat", "PInt"} {
		if inspect.IsAlias(inspect.Symbol(p, name)) {
			return name + " should be defined"
		}
	}
	// a '=' in a comment is not an alias declaration.
	if inspect.IsAlias(inspect.Symbol(p, "Tricky")) {
		return "comment '=' misread as alias"
	}
	// enum-ness is a usage property, not a declaration form: an alias
	// can still type constants.
	ae := inspect.Symbol(p, "AliasEnum")
	if !inspect.IsAlias(ae) {
		return "AliasEnum should be an alias"
	}
	if len(inspect.EnumMembers(ae)) != 1 {
		return "alias enum members lost"
	}
	return "ok"
}

// ---- trap checkers: each must surface an intrinsic error Go-side
// (scripts cannot catch traps) ----

// DefVarTrap: Def on a var decl is not a type.
func DefVarTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.Def(inspect.Symbol(p, "Count"))
	return "swallowed"
}

// ResolveBoundTrap: Resolve cannot descend into a bound package.
func ResolveBoundTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.Resolve(inspect.Fields(inspect.Symbol(p, "User"))[3].Type)
	return "swallowed"
}

// MissingSymTrap: Symbol on an unknown name errors.
func MissingSymTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.Symbol(p, "Nope")
	return "swallowed"
}

// BoundFieldTrap: Fields on a bound type has no decl to read.
func BoundFieldTrap() string {
	inspect.Fields(inspect.Symbol(inspect.PackageOf("strings"), "Builder"))
	return "swallowed"
}

// BoundMethodTrap: Methods on a bound type has no index to read.
func BoundMethodTrap() string {
	inspect.Methods(inspect.Symbol(inspect.PackageOf("strings"), "Builder"))
	return "swallowed"
}

// HostSigTrap: Signature on an intrinsic without a Target errors.
func HostSigTrap() string {
	inspect.Signature(inspect.SymbolOf(strings.Compare))
	return "swallowed"
}

// ImportRefTrap: an import ref keeps strict namespace semantics —
// `strings.Path` is not a field read (Go rejects it too).
func ImportRefTrap() string {
	return strings.Path
}

// PkgUnknownTrap: a name that is neither member nor field still
// traps "undefined: pkg.Nope".
func PkgUnknownTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	return p.Nope
}

// PkgUnexportedTrap: unexported names trap (they are neither members
// nor reachable metadata).
func PkgUnexportedTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	return p.path
}

// PkgDirTrap: a metadata field name that is not a package member
// traps "undefined: pkg.Dir (… use inspect.Dir(pkg))" — field access
// never falls back to host semantics.
func PkgDirTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	return p.Dir
}

// CurPkgPathTrap: the same miss on an unshadowed package — the
// reported issue's `d.Package.Path` shape stays a loud trap.
func CurPkgPathTrap() string {
	return inspect.Current().Path
}

// EnumMembersFuncTrap: EnumMembers is a type-symbol view.
func EnumMembersFuncTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.EnumMembers(inspect.Symbol(p, "Hello"))
	return "swallowed"
}

// EnumMembersBoundTrap: a bound type has no index to read.
func EnumMembersBoundTrap() string {
	inspect.EnumMembers(inspect.Symbol(inspect.PackageOf("strings"), "Builder"))
	return "swallowed"
}

// DeclTypeFuncTrap: DeclType is a value-spec view — funcs trap.
func DeclTypeFuncTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.DeclType(inspect.Symbol(p, "Hello"))
	return "swallowed"
}

// DeclTypeTypeTrap: DeclType on a type decl traps — Def reads types.
func DeclTypeTypeTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.DeclType(inspect.Symbol(p, "Status"))
	return "swallowed"
}

// IsAliasFuncTrap: IsAlias is a type-symbol view.
func IsAliasFuncTrap() string {
	p := inspect.DirOf("./testdata/inspectpkg")
	inspect.IsAlias(inspect.Symbol(p, "Hello"))
	return "swallowed"
}

// IsAliasBoundTrap: a bound type carries no declaration.
func IsAliasBoundTrap() string {
	inspect.IsAlias(inspect.Symbol(inspect.PackageOf("strings"), "Builder"))
	return "swallowed"
}

func main() {}
