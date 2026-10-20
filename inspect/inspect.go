// Package inspect lets minigo scripts examine Go declarations, syntax and values.
//
// In scripts, import "minigo.dev/inspect" or this module path and start with
// [PackageOf] and [Decls]:
//
//	p := inspect.PackageOf("example.com/app/model")
//	for _, d := range inspect.Decls(p) {
//		println(d.Name)
//	}
//
// These script functions trap on errors and must not be called from compiled
// Go. Host code uses the view methods, constructors and XxxOf variants such
// as [FieldsOf], which return errors instead. Cross-package lookups take a
// [Resolver].
//
// [PackageOf] prefers a bound package; [SourceOf] reads the source behind
// that binding, while [BoundOf] only reports the binding without loading
// source. Bound packages have no source syntax: [Decls] returns Kind "host"
// views, and syntax operations trap. A package mode that denies an import
// does not stop [SourceOf] from reading a bound path's source. Lookup
// variants such as [LookupPackageOf] return nil for missing targets; load
// failures and denied imports still trap. Source loading tolerates
// duplicate declarations.
//
// [Methods] lists declared methods; [MethodSet] includes promoted methods.
// [Fields] reads a type declaration; [TypeFields] reads a type expression,
// including anonymous structs and interfaces. Read package metadata through
// accessors such as [Path] and [Name]; a package's own members shadow it.
package inspect

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// View types are returned to scripts boxed as host values, so their
// exported fields are read through reflective member dispatch.
// Unexported fields keep the AST context (declaring file, expr) that
// functions like SymbolID and UnWrap resolve against; they are
// intentionally out of reach of scripts.

// BuiltinPackagePath is the pseudo package path of the SymbolID that
// predeclared identifiers (int, error, any, ...) resolve to.
const BuiltinPackagePath = ":builtin:"

// TypeParamPackagePath is the pseudo package path of the SymbolID that a
// type parameter resolves to, so a walk can tell "bound by the enclosing
// generic declaration" from a package-level type. Nothing is declared
// behind it: [Resolve] reports nil and [UnWrap] stops there, as for
// [BuiltinPackagePath].
const TypeParamPackagePath = ":typeparam:"

// NoDeclPath reports whether path is one of the pseudo paths
// ([BuiltinPackagePath], [TypeParamPackagePath]) that name no loadable
// package, so a walk following SymbolIDs knows where to stop.
func NoDeclPath(path string) bool {
	return path == BuiltinPackagePath || path == TypeParamPackagePath
}

// Decl describes one declaration: a top-level func, type, var or const, a
// method, or a member of a host-bound package (Kind "host"). Host decls
// have no source, so functions that read syntax trap on them; [Signature]
// still works when the host signature is known.
type Decl struct {
	// Package is the declaring package; read its metadata with
	// inspect.Path, inspect.Name and the other package accessors.
	Package *runtime.Package
	Kind    string    // "func", "method", "var", "const", "type" or "host"
	Name    string    // the declared name; a method's name without its receiver
	File    string    // declaring file name ("" for host symbols)
	Pos     *Position // declaring position (nil for most host symbols)
	Doc     string    // doc comment text, trailing newline trimmed ("" for host symbols)

	// Decl is the script API's "Symbol", renamed to avoid colliding with
	// the Symbol stub function.
	decl  *index.Decl
	file  *syntax.File
	hsig  *Sig // synthesized host signature (BuiltinFunc.Target)
	htype reflect.Type
}

// NewDecl returns the [Decl] view of d, a declaration indexed in pkg.
// Members of a host-bound package use [NewHostDecl] instead.
func NewDecl(pkg *runtime.Package, d *index.Decl) *Decl {
	s := &Decl{Package: pkg, decl: d}
	if d == nil {
		s.Kind = "host"
		return s
	}
	s.Name = d.Name
	if d.File != nil {
		s.file = d.File
		s.File = d.File.Name
	}
	if pkg != nil && pkg.Fset != nil {
		s.Pos = posOf(pkg.Fset, d.Pos)
	}
	switch d.Kind {
	case index.FuncDecl:
		if d.Func != nil && d.Func.Recv != nil {
			s.Kind = "method"
		} else {
			s.Kind = "func"
		}
		if d.Func != nil {
			s.Doc = docText(d.Func.Doc)
		}
	case index.TypeDecl:
		s.Kind = "type"
		if ts, ok := d.Spec.(*ast.TypeSpec); ok && ts.Doc != nil {
			s.Doc = docText(ts.Doc)
		} else {
			s.Doc = docText(d.Gen.Doc)
		}
	case index.VarDecl, index.ConstDecl:
		if d.Kind == index.VarDecl {
			s.Kind = "var"
		} else {
			s.Kind = "const"
		}
		if vs, ok := d.Spec.(*ast.ValueSpec); ok && vs.Doc != nil {
			s.Doc = docText(vs.Doc)
		} else {
			s.Doc = docText(d.Gen.Doc)
		}
	default:
		s.Kind = "decl"
	}
	return s
}

// NewHostDecl returns a Kind "host" [Decl] for the member name of a
// host-bound package. sig and target may be nil when the member's
// signature or Go type is unknown; [SignatureOf] then reports an error.
func NewHostDecl(pkg *runtime.Package, name string, sig *Sig, target reflect.Type) *Decl {
	return &Decl{Package: pkg, Kind: "host", Name: name, hsig: sig, htype: target}
}

// File is one source file of a package.
type File struct {
	Name string
	Doc  string // the package clause's doc comment, trailing newline trimmed
	// PkgName is the name in the file's own package clause. A directory
	// mixing package clauses still loads every file, so compare it with
	// the package's name to find foreign files.
	PkgName string

	pkg  *runtime.Package
	sf   *syntax.File
	fset *token.FileSet
}

// NewFile returns the [File] view of sf, a source file of pkg.
func NewFile(pkg *runtime.Package, sf *syntax.File) *File {
	f := &File{pkg: pkg, sf: sf, fset: pkg.Fset}
	f.Name = sf.Name
	if sf.AST != nil {
		if sf.AST.Name != nil {
			f.PkgName = sf.AST.Name.Name
		}
		if sf.AST.Doc != nil {
			f.Doc = docText(sf.AST.Doc)
		}
	}
	return f
}

// Position is a source position, the script-readable form of
// [token.Position]. Column is 0 when unknown.
type Position struct {
	File   string
	Line   int
	Column int
}

// String formats p as "file:line:col", or "file:line" when the column is
// unknown. A nil p formats as "".
func (p *Position) String() string {
	if p == nil {
		return ""
	}
	if p.Column > 0 {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
	}
	return fmt.Sprintf("%s:%d", p.File, p.Line)
}

// posOf structures a token.Pos; nil when the fset can't resolve it.
func posOf(fset *token.FileSet, pos token.Pos) *Position {
	if fset == nil {
		return nil
	}
	tp := fset.Position(pos)
	if tp.Filename == "" {
		return nil
	}
	return &Position{File: tp.Filename, Line: tp.Line, Column: tp.Column}
}

// Comment is one // or /* */ comment of a file.
type Comment struct {
	// Text is the comment exactly as written, markers included, so
	// annotations (// swagger:route) and directives (//go:generate)
	// can be matched on it.
	Text string
	Pos  *Position
	// Free reports that the comment is not attached to the file, a
	// declaration or a field: it sits inside a function body, floats
	// between declarations, or trails the file. Doc comments and line
	// comments report false.
	Free bool
}

// Import is one entry of a file's import table.
type Import struct {
	Path string
	// Name is the name the import binds in the file: the alias if any,
	// else the last path element (without a /vN suffix), which may
	// differ from the imported package's declared name.
	Name string
	Pos  *Position

	ref *runtime.ImportRef // materializes on demand
}

// NewImport returns the [Import] view of imp. ref may be nil.
func NewImport(fset *token.FileSet, imp *syntax.Import, ref *runtime.ImportRef) *Import {
	i := &Import{Path: imp.Path, Name: imp.LocalName(), ref: ref}
	if fset != nil {
		i.Pos = posOf(fset, imp.Pos)
	}
	return i
}

// Ref returns the package reference the engine bound for this import,
// or nil if there is none.
func (i *Import) Ref() *runtime.ImportRef { return i.ref }

// RepoPackage is one package directory reported by [Packages]: the
// module that claims it, the import path that module gives it, and its
// files split into GoFiles and TestFiles.
type RepoPackage = resolve.RepoPackage

// Field is one written field group: a struct field, an interface
// element, a parameter, result or receiver, or a type parameter.
// `a, b int` is one Field with two Names.
type Field struct {
	Names    []string // empty for embedded fields and unnamed params
	Type     *TypeExpr
	Tag      string // struct tag literal without its backquotes
	Doc      string // doc comment, else line comment; trailing newline trimmed
	Embedded bool
	Pos      *Position
}

// Sig is the script API's "Signature", renamed to avoid colliding with
// the Signature stub function. Params and Results stay boxed for script
// member dispatch.

// Sig is the signature of a func or method. Params and Results hold one
// *[Field] per written field group, so use [Sig.NumParams] and
// [Sig.NumResults] for arity. Signatures of host symbols have unnamed
// fields.
type Sig struct {
	Recv    *Field // nil for plain funcs
	Params  *runtime.Slice
	Results *runtime.Slice
}

// ParamFields returns Params as a Go slice.
func (s *Sig) ParamFields() []*Field { return unboxFields(s.Params) }

// ResultFields returns Results as a Go slice.
func (s *Sig) ResultFields() []*Field { return unboxFields(s.Results) }

// NumParams reports how many parameters the function takes: `a, b int`
// counts as two, an unnamed or variadic parameter as one. len(Params)
// counts field groups instead and under-reports.
func (s *Sig) NumParams() int {
	ts, _ := expandFieldTypes(s.ParamFields())
	return len(ts)
}

// NumResults is [Sig.NumParams] for the results: `(q, r int)` counts as
// two.
func (s *Sig) NumResults() int {
	ts, _ := expandFieldTypes(s.ResultFields())
	return len(ts)
}

// TypeExpr is an expression as written in source, usually a type, with
// the file context needed to resolve the names it mentions. A TypeExpr for
// a host symbol is backed by its Go type: Kind is "reflect:" plus the
// reflect kind and Text is the Go type string.
type TypeExpr struct {
	Text string // source spelling of the expression
	Kind string // go/ast node name: "Ident", "SelectorExpr", "StarExpr", ...

	expr    ast.Expr
	file    *syntax.File
	pkg     *runtime.Package
	locals  map[string]bool // names bound inside the enclosing func, when the expr was anchored via Expr — a hit means "possibly local", so SymbolID refuses package attribution rather than misattributing a method call to an import
	tparams map[string]bool // names the enclosing generic decl binds as type parameters — a hit reports the TypeParamPackagePath SymbolID instead of attributing the name to the package
	nonRef  bool            // an identifier in a position that names no declaration (selector Sel, label, declared name, struct-literal key), so SymbolID reports nil
	ht      reflect.Type    // host-backed alternative (BuiltinFunc.Target sigs)
}

// NewTypeExpr returns the [TypeExpr] view of e, an expression in file f
// of package p.
func NewTypeExpr(e ast.Expr, f *syntax.File, p *runtime.Package) *TypeExpr {
	te := &TypeExpr{expr: e, file: f, pkg: p}
	if e != nil {
		te.Kind = reflect.TypeOf(e).Elem().Name()
		var buf bytes.Buffer
		fset := token.NewFileSet()
		if p != nil && p.Fset != nil {
			fset = p.Fset
		}
		if err := printer.Fprint(&buf, fset, e); err == nil {
			te.Text = buf.String()
		}
	}
	return te
}

// NewHostType returns a [TypeExpr] backed by the Go type t, as used for
// host symbols.
func NewHostType(t reflect.Type) *TypeExpr {
	te := &TypeExpr{ht: t}
	if t != nil {
		te.Kind = "reflect:" + t.Kind().String()
		te.Text = t.String()
	}
	return te
}

// Resolver returns the [Decl] a SymbolID names, loading its package as
// needed. Host code supplies its own, typically by loading the package
// and looking the name up in it.
type Resolver func(sid runtime.SymbolID) (*Decl, error)

// docText flattens a comment group and drops the trailing newline
// CommentGroup.Text always appends, so emitters can place the text
// directly instead of trimming.
func docText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.TrimRight(g.Text(), "\n")
}

func fieldList(fl *ast.FieldList, f *syntax.File, p *runtime.Package, tps map[string]bool) []*Field {
	if fl == nil {
		return nil
	}
	var out []*Field
	for _, fd := range fl.List {
		fv := &Field{Type: NewTypeExpr(fd.Type, f, p).withTypeParams(tps), Doc: fieldDoc(fd)}
		for _, n := range fd.Names {
			fv.Names = append(fv.Names, n.Name)
		}
		fv.Embedded = len(fd.Names) == 0
		if fd.Tag != nil {
			fv.Tag = strings.Trim(fd.Tag.Value, "`")
		}
		if p != nil && p.Fset != nil {
			fv.Pos = posOf(p.Fset, fd.Pos())
		}
		out = append(out, fv)
	}
	return out
}

func fieldDoc(fd *ast.Field) string {
	if fd.Doc != nil {
		return docText(fd.Doc)
	}
	if fd.Comment != nil {
		return docText(fd.Comment)
	}
	return ""
}

func boxFields(fs []*Field) *runtime.Slice {
	xs := make([]runtime.Value, len(fs))
	for i, f := range fs {
		xs[i] = &runtime.GoValue{V: f}
	}
	return &runtime.Slice{Elems: xs}
}

// unboxFields reverses boxFields for host callers — elements that are
// not *Field views come back nil.
func unboxFields(s *runtime.Slice) []*Field {
	if s == nil {
		return nil
	}
	out := make([]*Field, len(s.Elems))
	for i, e := range s.Elems {
		if gv, ok := e.(*runtime.GoValue); ok {
			out[i], _ = gv.V.(*Field)
		}
	}
	return out
}

// FieldsOf is [Fields] for Go code, returning an error where Fields traps.
func FieldsOf(s *Decl) ([]*Field, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.Fields: host symbol %s has no declaration", s.Name)
	}
	ts, ok := s.decl.Spec.(*ast.TypeSpec)
	if !ok {
		return nil, fmt.Errorf("inspect.Fields: %s is not a type", s.Name)
	}
	tps := typeParamNames(ts.TypeParams)
	switch t := ts.Type.(type) {
	case *ast.StructType:
		return fieldList(t.Fields, s.file, s.Package, tps), nil
	case *ast.InterfaceType:
		return fieldList(t.Methods, s.file, s.Package, tps), nil
	}
	return nil, fmt.Errorf("inspect.Fields: %s is not a struct or interface type", s.Name)
}

// MReqsOf is [MReqs] for Go code, returning an error where MReqs traps.
func MReqsOf(s *Decl) ([]*Field, error) {
	it, err := ifaceOf("MReqs", s)
	if err != nil || it == nil {
		return nil, err
	}
	tps := declTypeParams(s)
	var out []*Field
	for _, fv := range fieldList(it.Methods, s.file, s.Package, tps) {
		if !fv.Embedded {
			out = append(out, fv)
		}
	}
	return out, nil
}

// IEmbedsOf is [IEmbeds] for Go code, returning an error where IEmbeds
// traps.
func IEmbedsOf(s *Decl) ([]*TypeExpr, error) {
	it, err := ifaceOf("IEmbeds", s)
	if err != nil || it == nil {
		return nil, err
	}
	tps := declTypeParams(s)
	var out []*TypeExpr
	for _, fv := range fieldList(it.Methods, s.file, s.Package, tps) {
		if fv.Embedded {
			out = append(out, fv.Type)
		}
	}
	return out, nil
}

// ifaceOf unwraps an interface type decl.
func ifaceOf(op string, s *Decl) (*ast.InterfaceType, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.%s: host symbol %s has no declaration", op, s.Name)
	}
	ts, ok := s.decl.Spec.(*ast.TypeSpec)
	if !ok {
		return nil, fmt.Errorf("inspect.%s: %s is not a type", op, s.Name)
	}
	it, ok := ts.Type.(*ast.InterfaceType)
	if !ok {
		// a non-interface *type* decl has no requirements or embeds —
		// report empty rather than trapping, so scripts don't gate
		// behind Def(d).Kind. Non-type decls still trap above.
		return nil, nil
	}
	return it, nil
}

// MethodsOf is [Methods] on a type decl, for Go code: it returns the
// type's declared methods sorted by name, or an error where Methods
// traps. [MethodsIn] covers the package and file forms.
func MethodsOf(s *Decl) ([]*Decl, error) {
	if s.decl == nil || s.Package == nil || s.Package.Index == nil {
		return nil, fmt.Errorf("inspect.Methods: %s has no method index", s.Name)
	}
	td, ok := s.Package.Index.Types[s.Name]
	if !ok {
		return nil, fmt.Errorf("inspect.Methods: %s is not a type", s.Name)
	}
	var out []*Decl
	for _, md := range td.Methods {
		out = append(out, NewDecl(s.Package, md))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name }) // map order is not stable
	return out, nil
}

// MethodsIn is [Methods] on a package or file, for Go code: it returns
// every method declared in p, in source order, or only those declared in
// file when file is non-nil. A host-bound package reports an error.
func MethodsIn(p *runtime.Package, file *syntax.File) ([]*Decl, error) {
	// The index's flat method list is used, so declarations the per-type
	// name maps drop (a second same-name method on one receiver, or a
	// method on a receiver with no type decl) still enumerate.
	if p == nil || p.Index == nil {
		return nil, fmt.Errorf("inspect.Methods: package has no method index")
	}
	var out []*Decl
	for _, md := range p.Index.Methods {
		if file != nil && md.File != file {
			continue
		}
		out = append(out, NewDecl(p, md))
	}
	return out, nil
}

// Method is a member of a type's method set.
type Method struct {
	Name string
	Sig  *Sig
	Decl *Decl // the method declaration; nil for an interface method spec
	// Via is the type declaring the member: the embedded type a method
	// was promoted from, or the interface declaring a method spec. It
	// is nil for a method declared on the queried type itself.
	Via *Decl
}

// MethodSetOf is [MethodSet] for Go code, returning an error where
// MethodSet traps. res resolves embedded types.
func MethodSetOf(s *Decl, res Resolver) ([]*Method, error) {
	if s.decl == nil || s.Package == nil || s.Package.Index == nil {
		return nil, fmt.Errorf("inspect.MethodSet: %s has no index", s.Name)
	}
	td, ok := s.Package.Index.Types[s.Name]
	if !ok {
		return nil, fmt.Errorf("inspect.MethodSet: %s is not a type", s.Name)
	}
	visited := map[runtime.SymbolID]bool{}
	var out []*Method
	// winners records the depth and declaring-decl key that claimed
	// each member name; ambig collects names claimed by two DISTINCT
	// declaring decls at the winning depth — ambiguous selectors,
	// which Go excludes from the set. Arrivals at a deeper level lose
	// silently, and the same declaring decl reached through two paths
	// (a diamond embed) counts once, not as a conflict.
	type win struct {
		depth int
		key   string
	}
	winners := map[string]win{}
	ambig := map[string]bool{}
	record := func(name string, depth int, key string) bool {
		if w, ok := winners[name]; ok {
			if w.depth == depth && w.key != key {
				ambig[name] = true // a distinct member at the same depth
			}
			return false
		}
		winners[name] = win{depth, key}
		return true
	}
	// queue entries are the decls that own members, one per resolved
	// embedded field — walked breadth-first so promotion depth, not
	// field order, decides which spelling a name keeps.
	type embedDecl struct {
		td    *index.TypeDeclInfo
		owner *Decl // the resolved embedded decl — fields and specs read from it
		via   *Decl // the decl this item was promoted from (nil for the queried type)
		ptr   bool  // the path down to owner crossed a pointer embed
		depth int
	}
	queue := []embedDecl{{td, s, nil, false, 0}}
	// an alias borrows its target's set — GB = GreetBase carries
	// GreetBase's methods identically, pointer receivers included, so
	// the target walks with a nil via (the alias IS the type).
	if ts, ok := s.decl.Spec.(*ast.TypeSpec); ok && ts.Assign.IsValid() {
		if ed := chaseType(NewTypeExpr(ts.Type, s.file, s.Package).withTypeParams(typeParamNames(ts.TypeParams)), visited, res); ed != nil {
			if ntd, ok := ed.Package.Index.Types[ed.Name]; ok {
				queue = append(queue, embedDecl{ntd, ed, nil, false, 0})
			}
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if ts, ok := cur.owner.decl.Spec.(*ast.TypeSpec); ok {
			if it, ok := ts.Type.(*ast.InterfaceType); ok {
				// embedded interfaces promote their method specs — all
				// at this decl's depth: interface embedding flattens
				// into the set, it does not add a promotion hop.
				promoteIfaceSpecs(it, cur.owner, cur.depth, res, record, visited, &out)
				continue
			}
		}
		for _, md := range cur.td.Methods {
			m := NewDecl(cur.owner.Package, md)
			sig, err := SignatureOf(m)
			if err != nil {
				continue
			}
			if !cur.ptr && cur.via != nil && sig.Recv != nil && sig.Recv.Type.Kind == "StarExpr" {
				continue // value method sets skip pointer receivers
			}
			if !record(md.Name, cur.depth, declKey(cur.owner)) {
				continue
			}
			out = append(out, &Method{Name: md.Name, Sig: sig, Decl: m, Via: cur.via})
		}
		fs, err := FieldsOf(cur.owner)
		if err != nil {
			continue // not a struct — nothing to promote from
		}
		for _, fd := range fs {
			if !fd.Embedded {
				continue
			}
			sub, byPtr := fd.Type, false
			if sub.Kind == "StarExpr" {
				byPtr = true
				sub = sub.Unref()
			}
			// resolve the embedded spelling to the decl that owns the
			// members — alias layers chase to the target, and the
			// instantiation base covers Pair[int]-style embeds.
			ed := chaseType(sub, visited, res)
			if ed == nil {
				continue
			}
			if ntd, ok := ed.Package.Index.Types[ed.Name]; ok {
				// pointer-ness accumulates: a pointer embed anywhere on
				// the path down keeps deeper pointer receivers visible.
				queue = append(queue, embedDecl{ntd, ed, ed, cur.ptr || byPtr, cur.depth + 1})
			}
		}
	}
	// ambiguous selectors drop out entirely: the winner recorded
	// earlier was only provisionally emitted.
	if len(ambig) > 0 {
		kept := out[:0]
		for _, m := range out {
			if !ambig[m.Name] {
				kept = append(kept, m)
			}
		}
		out = kept
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// declKey identifies the decl a member was read from: two paths
// reaching the same decl provide the same member (a diamond, not a
// conflict), while distinct decls provide distinct members.
func declKey(d *Decl) string { return d.Package.Path + "." + d.Name }

// chaseType follows a type spelling to the decl that owns it: a named
// reference resolves directly, instantiation bases (Pair[int]) and
// further named layers (defined types and aliases) keep chasing.
// Returns nil when the name leaves the index — builtins, foreign
// packages, type parameters, or a cycle through the visited set.
func chaseType(sub *TypeExpr, visited map[runtime.SymbolID]bool, res Resolver) *Decl {
	for i := 0; i < 8; i++ {
		sid, ok := sub.SymbolID()
		if !ok {
			// only the composite spellings that can denote the embedded
			// or aliased named type chase a decl: *T (borrowing *T's
			// set means T's methods), (T), and the base of a generic
			// instantiation F[A]. Other composites — slices, maps,
			// chans, func types, constraint elements — name no type, so
			// there is no method set to borrow.
			switch sub.expr.(type) {
			case *ast.StarExpr, *ast.ParenExpr, *ast.IndexExpr, *ast.IndexListExpr:
				sub = sub.Children()[0]
				continue
			}
			return nil
		}
		if NoDeclPath(sid.PackagePath) || visited[sid] {
			return nil
		}
		visited[sid] = true
		d, err := res(sid)
		if err != nil || d == nil || d.decl == nil {
			return nil
		}
		ts, ok := d.decl.Spec.(*ast.TypeSpec)
		if !ok {
			return nil
		}
		switch ts.Type.(type) {
		case *ast.Ident, *ast.SelectorExpr:
			sub = NewTypeExpr(ts.Type, d.file, d.Package).withTypeParams(typeParamNames(ts.TypeParams))
			continue // another named layer — keep chasing
		}
		return d
	}
	return nil
}

// errorSpec is the synthesized method spec an embedded `error`
// element contributes: Error() string.
var errorSpec = &ast.FuncType{Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}}}

// promoteIfaceSpecs lists an interface's members as method-set
// entries: the shared walker yields every reachable named spec,
// which becomes a spec-backed Method (no Decl). Spec members claim
// their name at the depth the interface was embedded — interface
// flattening adds no promotion hop — and the record hook decides
// shadowing and ambiguity exactly like declared members. Constraint
// elements are irrelevant on this side — they promote nothing — so
// the walker's error is ignored.
func promoteIfaceSpecs(it *ast.InterfaceType, owner *Decl, depth int, res Resolver, record func(name string, depth int, key string) bool, visited map[runtime.SymbolID]bool, out *[]*Method) {
	_ = walkIfaceSpecs(it, owner, res, visited, func(name string, ft *ast.FuncType, src *Decl) {
		if !record(name, depth, declKey(src)) {
			return
		}
		tps := declTypeParams(src)
		*out = append(*out, &Method{
			Name: name,
			Sig: &Sig{
				Params:  boxFields(fieldList(ft.Params, src.file, src.Package, tps)),
				Results: boxFields(fieldList(ft.Results, src.file, src.Package, tps)),
			},
			Via: src,
		})
	})
}

// ImplementersOf is [Implementers] for Go code, returning an error where
// Implementers traps. res resolves embedded interfaces and types.
func ImplementersOf(p *runtime.Package, iface *Decl, res Resolver) ([]*Decl, error) {
	// The required specs are flattened the same way promoteIfaceSpecs
	// flattens the candidate side.
	if p == nil || p.Index == nil {
		name := "<nil>"
		if p != nil {
			name = p.Path
		}
		return nil, fmt.Errorf("inspect.Implementers: %s has no index", name)
	}
	it, err := ifaceOf("Implementers", iface)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return nil, fmt.Errorf("inspect.Implementers: %s is not an interface type", iface.Name)
	}
	var specs []ifaceSpec
	if err := requiredSpecs(it, iface, res, map[runtime.SymbolID]bool{}, &specs); err != nil {
		return nil, err
	}
	var out []*Decl
	for _, d := range p.Index.Decls {
		if d.Kind != index.TypeDecl {
			continue
		}
		td := NewDecl(p, d)
		ms, err := MethodSetOf(td, res)
		if err != nil {
			return nil, err
		}
		if covers(ms, specs, res) {
			out = append(out, td)
		}
	}
	return out, nil
}

// requiredSpecs collects an interface's required method specs
// transitively through the shared walker — Talker{ Greeter; Talk() }
// carries Greet too, and an embedded `error` requires its Error()
// string spec. Elements that make the interface a constraint — ~T
// terms, A|B unions, embedded non-interface decls, other builtins —
// report an error instead of silently dropping the requirement.
// Unresolvable package paths contribute nothing.
func requiredSpecs(it *ast.InterfaceType, owner *Decl, res Resolver, visited map[runtime.SymbolID]bool, specs *[]ifaceSpec) error {
	return walkIfaceSpecs(it, owner, res, visited, func(name string, ft *ast.FuncType, src *Decl) {
		*specs = append(*specs, ifaceSpec{name: name, ft: ft, file: src.file, pkg: src.Package, tps: declTypeParams(src)})
	})
}

// walkIfaceSpecs is the shared embedded-interface flattener for both
// the requirement side (requiredSpecs) and the candidate side
// (promoteIfaceSpecs): it yields every named method spec reachable
// from it — own specs, embedded interface literals flattened in
// place, and embedded named interfaces resolved transitively
// (aliases chase to their target decl). A builtin `error` element
// yields the synthesized Error() string spec; `any` contributes
// nothing. Elements that turn the interface into a constraint (~T
// terms, unions, embedded non-interface decls, other builtins) are
// skipped but recorded: the first such error returns after the walk
// completes, and the caller decides whether it is fatal — implementer
// checking must not silently drop requirements, method-set promotion
// can ignore it since constraint terms promote nothing.
func walkIfaceSpecs(it *ast.InterfaceType, owner *Decl, res Resolver, visited map[runtime.SymbolID]bool, yield func(name string, ft *ast.FuncType, owner *Decl)) error {
	var firstErr error
	constraint := func() {
		if firstErr == nil {
			firstErr = fmt.Errorf("inspect.Implementers: %s is a constraint interface — nothing can implement it", owner.Name)
		}
	}
	var walk func(it *ast.InterfaceType, owner *Decl)
	walk = func(it *ast.InterfaceType, owner *Decl) {
		for _, fd := range fieldList(it.Methods, owner.file, owner.Package, declTypeParams(owner)) {
			if !fd.Embedded {
				if ft, ok := fd.Type.Expr().(*ast.FuncType); ok {
					yield(fd.Names[0], ft, owner)
				}
				continue
			}
			switch fd.Type.Expr().(type) {
			case *ast.UnaryExpr, *ast.BinaryExpr: // ~T and union terms: a constraint, not an interface
				constraint()
				continue
			}
			if anon, ok := fd.Type.Expr().(*ast.InterfaceType); ok {
				walk(anon, owner) // an interface literal flattens in place
				continue
			}
			sid, ok := fd.Type.SymbolID()
			if !ok {
				continue // unspellable embedded element — nothing follows
			}
			if sid.PackagePath == TypeParamPackagePath {
				constraint() // an embedded type parameter is a type-set term
				continue
			}
			if sid.PackagePath == BuiltinPackagePath {
				switch sid.Name {
				case "error":
					yield("Error", errorSpec, owner)
				case "any":
					// any carries no requirement
				default:
					constraint() // comparable et al. make it a constraint
				}
				continue
			}
			ed := chaseType(fd.Type, visited, res)
			if ed == nil {
				continue // type params and foreign packages resolve to nothing
			}
			ts, ok := ed.decl.Spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			ie, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				constraint() // an embedded non-interface is a type-set term
				continue
			}
			walk(ie, ed)
		}
	}
	walk(it, owner)
	return firstErr
}

// ifaceSpec is one named method spec of an interface — what a type
// must provide to be an implementer — kept with the decl it was read
// from so signature comparisons resolve type names in the spec's own
// file context (flattened requirements span decls and packages).
type ifaceSpec struct {
	name string
	ft   *ast.FuncType
	file *syntax.File
	pkg  *runtime.Package
	tps  map[string]bool // type parameters the owning decl binds
}

// covers reports whether the method set ms satisfies every spec:
// each spec name must appear with an identical signature — parameters,
// results, and variadicity compared via SameType.
func covers(ms []*Method, specs []ifaceSpec, res Resolver) bool {
	for _, sp := range specs {
		found := false
		for _, m := range ms {
			if m.Name == sp.name && sigMatch(m, sp, res) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sigMatch compares a method-set member's signature against an
// interface spec's FuncType: parameter count, result count, variadic
// flag, and each position's type.
func sigMatch(m *Method, spec ifaceSpec, res Resolver) bool {
	if m.Sig == nil {
		return false
	}
	sp, sv := paramTypes(spec.ft.Params, spec.file, spec.pkg, spec.tps)
	sr, _ := paramTypes(spec.ft.Results, spec.file, spec.pkg, spec.tps)
	mp, mv := expandFieldTypes(m.Sig.ParamFields())
	mr, _ := expandFieldTypes(m.Sig.ResultFields())
	if sv != mv || len(sp) != len(mp) || len(sr) != len(mr) {
		return false
	}
	for i := range sp {
		if !specSame(sp[i], mp[i], res) {
			return false
		}
	}
	for i := range sr {
		if !specSame(sr[i], mr[i], res) {
			return false
		}
	}
	return true
}

// specSame mirrors SameType for spec/method signature positions,
// with one relaxation: alias spellings collapse to their target
// decls at every node — `type Str = string` IS string in Go, so a
// method spelled with the alias satisfies a spec spelled with the
// target (and vice versa). A defined type still does not collapse.
func specSame(a, b *TypeExpr, res Resolver) bool {
	a, b = collapseAlias(a, res), collapseAlias(b, res)
	sa, oka := a.SymbolID()
	sb, okb := b.SymbolID()
	if oka || okb {
		return oka && okb && sa == sb
	}
	if a.Kind != b.Kind {
		return false
	}
	if !a.sameShapeExtra(b, res) {
		return false
	}
	ca, cb := a.Children(), b.Children()
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if !specSame(ca[i], cb[i], res) {
			return false
		}
	}
	return true
}

// collapseAlias unwraps alias layers — `type Str = string`,
// `type S2 = Str` — until it reaches a non-alias decl, a builtin, or
// an unresolvable spelling. A defined type stays put: it is not its
// underlying type. The unwrapped expression is re-rooted in its own
// file context so SameType resolves it correctly.
func collapseAlias(te *TypeExpr, res Resolver) *TypeExpr {
	for i := 0; i < 8; i++ {
		sid, ok := te.SymbolID()
		if !ok || NoDeclPath(sid.PackagePath) {
			return te
		}
		d, err := res(sid)
		if err != nil || d == nil || d.decl == nil {
			return te
		}
		ts, ok := d.decl.Spec.(*ast.TypeSpec)
		if !ok || !ts.Assign.IsValid() {
			return te // a defined type does not collapse
		}
		te = NewTypeExpr(ts.Type, d.file, d.Package).withTypeParams(typeParamNames(ts.TypeParams))
	}
	return te
}

// paramTypes flattens an ast.FieldList to one type expression per
// declared parameter — a, b int yields two — in declaration order,
// flagging a trailing ellipsis as variadic. The Field view is the
// shared shape: it delegates to expandFieldTypes.
func paramTypes(fl *ast.FieldList, file *syntax.File, pkg *runtime.Package, tps map[string]bool) ([]*TypeExpr, bool) {
	if fl == nil {
		return nil, false
	}
	return expandFieldTypes(fieldList(fl, file, pkg, tps))
}

// expandFieldTypes flattens view Fields the same way — sig fields keep
// one Field per written field group, so a, b int expands to two.
func expandFieldTypes(fs []*Field) ([]*TypeExpr, bool) {
	var out []*TypeExpr
	variadic := false
	for i, fv := range fs {
		n := len(fv.Names)
		if n == 0 {
			n = 1
		}
		if i == len(fs)-1 && fv.Type.Kind == "Ellipsis" {
			variadic = true
		}
		for j := 0; j < n; j++ {
			out = append(out, fv.Type)
		}
	}
	return out, variadic
}

// EnumMembersOf is [EnumMembers] for Go code, returning an error where
// EnumMembers traps.
func EnumMembersOf(s *Decl) ([]*Decl, error) {
	// An explicit-type member whose value cannot be read still counts —
	// its membership is syntactic fact and the caller reports the
	// unreadable value — but a type-inferred candidate with no readable
	// value is not a member.
	if s.decl == nil || s.Package == nil || s.Package.Index == nil {
		return nil, fmt.Errorf("inspect.EnumMembers: %s has no index", s.Name)
	}
	if s.decl.Kind != index.TypeDecl {
		return nil, fmt.Errorf("inspect.EnumMembers: %s is a %s, not a type", s.Name, s.Kind)
	}
	var exported, unexported []*Decl
	for _, cd := range s.Package.Index.Decls {
		if cd.Kind != index.ConstDecl {
			continue
		}
		member := false
		evaluated := false
		var elem runtime.Value
		if t := valueSpecType(cd); t != nil {
			if sid, ok := NewTypeExpr(t, cd.File, s.Package).SymbolID(); ok &&
				sid.PackagePath == s.Package.Path && sid.Name == s.Name {
				member = true
			}
		} else if vs, ok := cd.Spec.(*ast.ValueSpec); ok && len(vs.Values) > 0 {
			// a spec without a type but with values — `Microsecond =
			// 1000 * Nanosecond` — carries its type only on the value,
			// so membership asks the materialized constant.
			elem, evaluated = constElemOf(s.Package, cd)
			if n, ok := elem.(*runtime.Named); ok && typedefMatches(n.Typ, s) {
				member = true
			}
		}
		if !member {
			continue
		}
		if !evaluated {
			elem, evaluated = constElemOf(s.Package, cd)
		}
		if evaluated && !isConstScalar(elem) {
			continue
		}
		if ast.IsExported(cd.Name) {
			exported = append(exported, NewDecl(s.Package, cd))
		} else {
			unexported = append(unexported, NewDecl(s.Package, cd))
		}
	}
	return append(exported, unexported...), nil
}

// constElemOf reads a const member's bound value: the const-only
// initializer binds every const spec before member reads, so a miss
// means the constant never materialized (broken init). An unreadable
// value is inconclusive, never proof of exclusion — the caller
// decides.
func constElemOf(p *runtime.Package, cd *index.Decl) (runtime.Value, bool) {
	m, err := p.Member(cd.Name, nil)
	if err != nil {
		return nil, false
	}
	if c, ok := m.(*runtime.Cell); ok {
		return c.Elem, true
	}
	return m, m != nil
}

// typedefMatches reports whether the typedef a materialized value
// carries is the enum type: by TypeSpec identity for a source typedef,
// by package path and name for a bound one.
func typedefMatches(td *runtime.TypeDef, s *Decl) bool {
	if td == nil {
		return false
	}
	if td.Spec != nil {
		return td.Spec == s.decl.Spec
	}
	return td.Pkg != nil && td.Pkg.Path == s.Package.Path && td.Name == s.Name
}

// isConstScalar reports whether a materialized const value is a
// constant scalar — the only thing a Go enum member can be: an
// untyped constant, a named-basic value (its payload is the scalar),
// a host-scalar box, or a native scalar. Composites, references and
// host non-scalars are not enum values.
func isConstScalar(v runtime.Value) bool {
	switch t := v.(type) {
	case *runtime.UConst, *runtime.Named:
		return true
	case *runtime.GoValue:
		return t.V != nil && scalarKind(reflect.TypeOf(t.V).Kind())
	}
	return v != nil && scalarKind(reflect.TypeOf(v).Kind())
}

func scalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	}
	return false
}

// valueSpecType returns the type expression declared on a var or
// const decl: the spec's explicit type, or the type an empty const
// spec inherits from the nearest non-empty spec above it. Untyped
// specs report nil.
func valueSpecType(d *index.Decl) ast.Expr {
	vs, ok := d.Spec.(*ast.ValueSpec)
	if !ok {
		return nil
	}
	if vs.Type != nil {
		return vs.Type
	}
	return d.InheritedType
}

// SignatureOf is [Signature] for Go code, returning an error where
// Signature traps.
func SignatureOf(s *Decl) (*Sig, error) {
	if s.hsig != nil {
		return s.hsig, nil
	}
	if s.decl == nil || s.decl.Func == nil {
		return nil, fmt.Errorf("inspect.Signature: %s has no signature", s.Name)
	}
	ft := s.decl.Func.Type
	tps := declTypeParams(s)
	sig := &Sig{
		Params:  boxFields(fieldList(ft.Params, s.file, s.Package, tps)),
		Results: boxFields(fieldList(ft.Results, s.file, s.Package, tps)),
	}
	if s.decl.Func.Recv != nil && len(s.decl.Func.Recv.List) > 0 {
		recv := s.decl.Func.Recv.List[0]
		fv := &Field{Type: NewTypeExpr(recv.Type, s.file, s.Package).withTypeParams(tps)}
		for _, n := range recv.Names {
			fv.Names = append(fv.Names, n.Name)
		}
		if s.Package != nil && s.Package.Fset != nil {
			fv.Pos = posOf(s.Package.Fset, recv.Pos())
		}
		sig.Recv = fv
	}
	return sig, nil
}

// TypeParamsOf is [TypeParams] for Go code, returning an error where
// TypeParams traps.
func TypeParamsOf(s *Decl) ([]*Field, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.TypeParams: %s is not a decl", s.Name)
	}
	var fl *ast.FieldList
	switch {
	case s.decl.Func != nil:
		fl = s.decl.Func.Type.TypeParams
	default:
		if ts, ok := s.decl.Spec.(*ast.TypeSpec); ok {
			fl = ts.TypeParams
		}
	}
	// a constraint may name a sibling parameter (S ~[]E, E any), so the
	// fields read their own list's names as bound.
	return fieldList(fl, s.file, s.Package, typeParamNames(fl)), nil
}

// DefOf is [Def] for Go code, returning an error where Def traps.
func DefOf(s *Decl) (*TypeExpr, error) {
	if s.decl == nil {
		if s.htype != nil {
			return NewHostType(s.htype), nil
		}
		return nil, fmt.Errorf("inspect.Def: %s is not a decl", s.Name)
	}
	ts, ok := s.decl.Spec.(*ast.TypeSpec)
	if !ok {
		return nil, fmt.Errorf("inspect.Def: %s is not a type", s.Name)
	}
	return NewTypeExpr(ts.Type, s.file, s.Package).withTypeParams(typeParamNames(ts.TypeParams)), nil
}

// DeclTypeOf is [DeclType] for Go code, returning an error where
// DeclType traps.
func DeclTypeOf(s *Decl) (*TypeExpr, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.DeclType: host symbol %s has no declaration", s.Name)
	}
	switch s.decl.Kind {
	case index.VarDecl, index.ConstDecl:
	default:
		return nil, fmt.Errorf("inspect.DeclType: %s is a %s, not a var/const", s.Name, s.Kind)
	}
	t := valueSpecType(s.decl)
	if t == nil {
		return nil, nil
	}
	return NewTypeExpr(t, s.file, s.Package), nil
}

// InitializerOf is [Initializer] for Go code, returning an error where
// Initializer traps.
func InitializerOf(s *Decl) (*TypeExpr, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.Initializer: host symbol %s has no declaration", s.Name)
	}
	switch s.decl.Kind {
	case index.VarDecl, index.ConstDecl:
	default:
		return nil, fmt.Errorf("inspect.Initializer: %s is a %s, not a var/const", s.Name, s.Kind)
	}
	vs, ok := s.decl.Spec.(*ast.ValueSpec)
	if !ok {
		return nil, nil
	}
	vals := vs.Values
	if len(vals) == 0 {
		vals = s.decl.Inherited
	}
	if len(vals) == 0 {
		return nil, nil
	}
	i := s.decl.NameIdx
	if i >= len(vals) {
		i = 0
	}
	return NewTypeExpr(vals[i], s.file, s.Package), nil
}

// IsAliasOf is [IsAlias] for Go code, returning an error where IsAlias
// traps.
func IsAliasOf(s *Decl) (bool, error) {
	if s.decl == nil {
		return false, fmt.Errorf("inspect.IsAlias: host symbol %s has no declaration", s.Name)
	}
	ts, ok := s.decl.Spec.(*ast.TypeSpec)
	if !ok {
		return false, fmt.Errorf("inspect.IsAlias: %s is a %s, not a type", s.Name, s.Kind)
	}
	return ts.Assign.IsValid(), nil
}

// Expr returns the underlying syntax node, or nil for a TypeExpr backed
// by a host type.
func (te *TypeExpr) Expr() ast.Expr { return te.expr }

// wrap builds a TypeExpr for a sub-expression in the same context —
// file, package, the enclosing decl's type parameters, and the
// enclosing func's local-name set when the expr was anchored inside a
// body.
func (te *TypeExpr) wrap(e ast.Expr) *TypeExpr {
	nt := NewTypeExpr(e, te.file, te.pkg)
	nt.locals = te.locals
	nt.tparams = te.tparams
	return nt
}

// withTypeParams records the type parameter names the enclosing decl
// binds, so SymbolID can mark the identifiers they name. It is the
// decl-level counterpart of the locals anchor Expr sets for bodies.
func (te *TypeExpr) withTypeParams(tps map[string]bool) *TypeExpr {
	te.tparams = tps
	return te
}

// typeParamNames collects the names a *ast.FieldList of type
// parameters binds — `T any`, `K comparable, V any` — as a set; the
// shared ordered walk lives in syntax.TypeParamNames.
func typeParamNames(fl *ast.FieldList) map[string]bool {
	var names map[string]bool
	for _, n := range syntax.TypeParamNames(fl) {
		if names == nil {
			names = map[string]bool{}
		}
		names[n] = true
	}
	return names
}

// declTypeParams collects the type parameter names bound by s's decl:
// a generic type or alias decl's TypeSpec.TypeParams; a generic func's
// FuncType.TypeParams plus — for a method — the names its receiver
// instantiation declares ((b Box[T]) binds T; Go has no method-local
// type parameter list).
func declTypeParams(s *Decl) map[string]bool {
	if s == nil || s.decl == nil {
		return nil
	}
	if fn := s.decl.Func; fn != nil {
		names := recvTypeParamNames(fn.Recv)
		if fn.Type != nil {
			for n := range typeParamNames(fn.Type.TypeParams) {
				if names == nil {
					names = map[string]bool{}
				}
				names[n] = true
			}
		}
		return names
	}
	if ts, ok := s.decl.Spec.(*ast.TypeSpec); ok {
		return typeParamNames(ts.TypeParams)
	}
	return nil
}

// recvTypeParamNames collects the type parameter names a method
// receiver's instantiation declares — `(p Pair[A, B])` binds A and B
// for the whole method body — as a set; the ordered walk (pointer/
// paren peel included) lives in syntax.RecvTypeParamNames.
func recvTypeParamNames(recv *ast.FieldList) map[string]bool {
	var names map[string]bool
	for _, n := range syntax.RecvTypeParamNames(recv) {
		if names == nil {
			names = map[string]bool{}
		}
		names[n] = true
	}
	return names
}

// Children is the method form of [Children].
func (te *TypeExpr) Children() []*TypeExpr {
	if te.ht != nil {
		return te.hostChildren()
	}
	wrap := te.wrap
	var out []*TypeExpr
	switch e := te.expr.(type) {
	case *ast.StarExpr:
		out = append(out, wrap(e.X))
	case *ast.ArrayType:
		out = append(out, wrap(e.Elt))
	case *ast.MapType:
		out = append(out, wrap(e.Key), wrap(e.Value))
	case *ast.ChanType:
		out = append(out, wrap(e.Value))
	case *ast.Ellipsis:
		out = append(out, wrap(e.Elt))
	case *ast.ParenExpr:
		out = append(out, wrap(e.X))
	case *ast.IndexExpr:
		out = append(out, wrap(e.X), wrap(e.Index))
	case *ast.IndexListExpr:
		out = append(out, wrap(e.X))
		for _, ix := range e.Indices {
			out = append(out, wrap(ix))
		}
	case *ast.UnaryExpr:
		out = append(out, wrap(e.X)) // ~T constraints
	case *ast.BinaryExpr:
		out = append(out, wrap(e.X), wrap(e.Y)) // A | B unions
	case *ast.FuncType:
		for _, fd := range fieldList(e.Params, te.file, te.pkg, te.tparams) {
			out = append(out, fd.Type)
		}
		for _, fd := range fieldList(e.Results, te.file, te.pkg, te.tparams) {
			out = append(out, fd.Type)
		}
	case *ast.StructType:
		for _, fd := range fieldList(e.Fields, te.file, te.pkg, te.tparams) {
			out = append(out, fd.Type)
		}
	case *ast.InterfaceType:
		for _, fd := range fieldList(e.Methods, te.file, te.pkg, te.tparams) {
			out = append(out, fd.Type)
		}
	}
	return out
}

func (te *TypeExpr) hostChildren() []*TypeExpr {
	t := te.ht
	var out []*TypeExpr
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		out = append(out, NewHostType(t.Elem()))
	case reflect.Map:
		out = append(out, NewHostType(t.Key()), NewHostType(t.Elem()))
	case reflect.Func:
		for i := 0; i < t.NumIn(); i++ {
			out = append(out, NewHostType(t.In(i)))
		}
		for i := 0; i < t.NumOut(); i++ {
			out = append(out, NewHostType(t.Out(i)))
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			out = append(out, NewHostType(t.Field(i).Type))
		}
	case reflect.Interface:
		for i := 0; i < t.NumMethod(); i++ {
			out = append(out, NewHostType(t.Method(i).Type))
		}
	}
	return out
}

// TypeFieldsOf is [TypeFields] for Go code, returning an error where
// TypeFields traps.
func TypeFieldsOf(te *TypeExpr) ([]*Field, error) {
	if te.ht != nil {
		return hostTypeFields(te.ht)
	}
	switch e := te.expr.(type) {
	case *ast.StructType:
		return fieldList(e.Fields, te.file, te.pkg, te.tparams), nil
	case *ast.InterfaceType:
		return fieldList(e.Methods, te.file, te.pkg, te.tparams), nil
	}
	return nil, fmt.Errorf("inspect.TypeFields: %s is not a struct or interface type expression", te.Kind)
}

func hostTypeFields(t reflect.Type) ([]*Field, error) {
	switch t.Kind() {
	case reflect.Struct:
		out := make([]*Field, 0, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			fv := &Field{Type: NewHostType(sf.Type), Tag: string(sf.Tag), Embedded: sf.Anonymous}
			if !sf.Anonymous {
				fv.Names = []string{sf.Name}
			}
			out = append(out, fv)
		}
		return out, nil
	case reflect.Interface:
		// reflect flattens embedded interfaces, so every method reads
		// as a named spec — the Embedded distinction is source-level.
		out := make([]*Field, 0, t.NumMethod())
		for i := 0; i < t.NumMethod(); i++ {
			m := t.Method(i)
			out = append(out, &Field{Names: []string{m.Name}, Type: NewHostType(m.Type)})
		}
		return out, nil
	}
	return nil, fmt.Errorf("inspect.TypeFields: host type %s is not a struct or interface", t)
}

// Unref is the method form of [UnRef].
func (te *TypeExpr) Unref() *TypeExpr {
	// Named Unref: UnRef is taken by the stub declaration.
	if te.ht != nil {
		if te.ht.Kind() == reflect.Pointer {
			return NewHostType(te.ht.Elem())
		}
		return te
	}
	if e, ok := te.expr.(*ast.StarExpr); ok {
		return te.wrap(e.X)
	}
	return te
}

// Unwrap is the method form of [UnWrap]; res resolves the named type.
func (te *TypeExpr) Unwrap(res Resolver) *TypeExpr {
	sid, ok := te.SymbolID()
	if !ok || NoDeclPath(sid.PackagePath) {
		return te
	}
	sym, err := res(sid)
	if err != nil || sym == nil || sym.decl == nil {
		return te
	}
	ts, ok := sym.decl.Spec.(*ast.TypeSpec)
	if !ok {
		return te
	}
	return NewTypeExpr(ts.Type, sym.file, sym.Package).withTypeParams(typeParamNames(ts.TypeParams))
}

// Origin is the method form of [Origin]; res resolves named types.
func (te *TypeExpr) Origin(res Resolver) *TypeExpr {
	seen := map[runtime.SymbolID]bool{}
	for {
		if u := te.Unref(); u != te {
			te = u
			continue
		}
		if sid, ok := te.SymbolID(); ok {
			if seen[sid] {
				return te
			}
			seen[sid] = true
		}
		if w := te.Unwrap(res); w != te {
			te = w
			continue
		}
		return te
	}
}

// SymbolID is the method form of [SymbolID]: ok is false where SymbolID
// reports nil.
func (te *TypeExpr) SymbolID() (runtime.SymbolID, bool) {
	if te.ht != nil {
		t := te.ht
		if t.Name() == "" || t.PkgPath() == "" {
			return runtime.SymbolID{PackagePath: BuiltinPackagePath, Name: t.String()}, true
		}
		return runtime.SymbolID{PackagePath: t.PkgPath(), Name: t.Name()}, true
	}
	switch e := te.expr.(type) {
	case *ast.Ident:
		if te.nonRef || e.Name == "_" || te.locals[e.Name] {
			return runtime.SymbolID{}, false
		}
		if te.tparams[e.Name] {
			// bound by the enclosing generic decl — marked, never
			// attributed to the package (a type parameter shadows any
			// same-named package type).
			return runtime.SymbolID{PackagePath: TypeParamPackagePath, Name: e.Name}, true
		}
		if _, ok := te.importOf(e.Name); ok {
			// a file-scope import binding (the strings of strings.X) names
			// a package, not a declaration in one.
			return runtime.SymbolID{}, false
		}
		if te.pkg != nil && te.pkg.Index != nil {
			ix := te.pkg.Index
			_, isType := ix.Types[e.Name]
			_, isFunc := ix.Funcs[e.Name]
			_, isVar := ix.Vars[e.Name]
			_, isConst := ix.Consts[e.Name]
			if isType || isFunc || isVar || isConst {
				// a package member shadows a same-named builtin.
				return runtime.SymbolID{PackagePath: te.pkg.Path, Name: e.Name}, true
			}
		}
		if syntax.IsPredeclaredType(e.Name) || syntax.IsPredeclaredValue(e.Name) {
			return runtime.SymbolID{PackagePath: BuiltinPackagePath, Name: e.Name}, true
		}
		if te.pkg != nil {
			return runtime.SymbolID{PackagePath: te.pkg.Path, Name: e.Name}, true
		}
		return runtime.SymbolID{PackagePath: BuiltinPackagePath, Name: e.Name}, true
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok || te.file == nil || te.locals[x.Name] || te.tparams[x.Name] {
			return runtime.SymbolID{}, false
		}
		if imp, ok := te.importOf(x.Name); ok {
			return runtime.SymbolID{PackagePath: imp.Path, Name: e.Sel.Name}, true
		}
		return runtime.SymbolID{}, false
	}
	return runtime.SymbolID{}, false
}

// importOf returns the import of te's file that binds name; dot and
// blank imports bind none.
func (te *TypeExpr) importOf(name string) (*syntax.Import, bool) {
	if te.file == nil {
		return nil, false
	}
	for _, imp := range te.file.Imports {
		if imp.Alias != "." && imp.Alias != "_" && imp.LocalName() == name {
			return imp, true
		}
	}
	return nil, false
}

// CanonicalName returns the type's identity independent of how a file
// spells it: "import/path.Name" for a named type, the bare name for a
// predeclared type or type parameter, and "*" plus that for a pointer to
// one. Other composite types (slices, maps, func types, ...) report "".
func (te *TypeExpr) CanonicalName() string {
	if te == nil {
		return ""
	}
	switch te.Kind {
	case "StarExpr", "reflect:ptr":
		inner := te.Unref().CanonicalName()
		if inner == "" {
			return ""
		}
		return "*" + inner
	default:
		if te.ht != nil && te.ht.Name() == "" {
			return "" // unnamed host composite — like a syntax composite
		}
		sid, ok := te.SymbolID()
		if !ok {
			return ""
		}
		if NoDeclPath(sid.PackagePath) {
			// builtins and type parameters render as the bare name — a
			// type parameter's identity is per-name only.
			return sid.Name
		}
		return sid.PackagePath + "." + sid.Name
	}
}

// SameType is the method form of [SameType]; res resolves names in
// array lengths. A name without a SymbolID matches only under the same
// [BodyOf] root, so one reached from a bare [NewNode] root (which knows
// no locals) does not even match itself.
func (a *TypeExpr) SameType(b *TypeExpr, res Resolver) bool {
	sa, oka := a.SymbolID()
	sb, okb := b.SymbolID()
	if oka && okb {
		return sa == sb
	}
	if oka != okb {
		return false
	}
	if a.Kind != b.Kind {
		return false
	}
	switch ea := a.expr.(type) {
	case *ast.Ident:
		// a name with no SymbolID is bound inside the enclosing func
		// (a local type): the same spelling under the same Body root
		// is the same type, anything else is unknown and reported as
		// different rather than matched by its empty shape.
		eb, ok := b.expr.(*ast.Ident)
		return ok && ea.Name == eb.Name && a.locals != nil &&
			reflect.ValueOf(a.locals).Pointer() == reflect.ValueOf(b.locals).Pointer()
	case *ast.SelectorExpr:
		return false // a member of a local value, not a resolvable type
	}
	if !a.sameShapeExtra(b, res) {
		return false
	}
	ca, cb := a.Children(), b.Children()
	if len(ca) != len(cb) {
		return false
	}
	for i := range ca {
		if !ca[i].SameType(cb[i], res) {
			return false
		}
	}
	return true
}

// sameShapeExtra compares the node fields Children does not expose:
// array length and channel direction ([2]int != [3]int,
// chan T != <-chan T).
func (a *TypeExpr) sameShapeExtra(b *TypeExpr, res Resolver) bool {
	switch ea := a.expr.(type) {
	case *ast.ArrayType:
		eb, ok := b.expr.(*ast.ArrayType)
		if !ok {
			return false
		}
		return sameArrayLen(a.Sub(ea.Len), b.Sub(eb.Len), res)
	case *ast.ChanType:
		eb, ok := b.expr.(*ast.ChanType)
		return ok && ea.Dir == eb.Dir
	}
	return true
}

// Sub returns the [TypeExpr] view of e, a sub-expression of
// [TypeExpr.Expr], resolved in the same context. Use it for parts
// [TypeExpr.Children] does not reach, such as an array length. A nil e
// yields nil.
func (te *TypeExpr) Sub(e ast.Expr) *TypeExpr {
	if e == nil {
		return nil
	}
	return te.wrap(e)
}

// sameArrayLen compares two array-length expressions: both nil is a
// slice; otherwise the constant values when evaluatable ([1+1]int ==
// [2]int), else the resolved symbol identity ([N]int), else spelling.
func sameArrayLen(a, b *TypeExpr, res Resolver) bool {
	if a == nil || b == nil {
		return a == b
	}
	va, oka := lenConst(a, res)
	vb, okb := lenConst(b, res)
	if oka && okb {
		return va == vb
	}
	if oka != okb {
		return false
	}
	sa, oka2 := a.SymbolID()
	sb, okb2 := b.SymbolID()
	if oka2 && okb2 {
		return sa == sb
	}
	if oka2 != okb2 {
		return false
	}
	return a.Text == b.Text
}

// lenConst evaluates a length expression, chasing ident/selector
// references into the const decl's value ([sizeN]int == [2]int when
// sizeN = 2). Inherited iota specs stay unevaluatable.
func lenConst(te *TypeExpr, res Resolver) (int64, bool) {
	if v, ok := constInt(te.expr); ok {
		return v, true
	}
	sid, ok := te.SymbolID()
	if !ok || NoDeclPath(sid.PackagePath) {
		return 0, false
	}
	sym, err := res(sid)
	if err != nil || sym == nil || sym.decl == nil {
		return 0, false
	}
	vs, ok := sym.decl.Spec.(*ast.ValueSpec)
	if !ok || sym.decl.NameIdx >= len(vs.Values) {
		return 0, false
	}
	return constInt(vs.Values[sym.decl.NameIdx])
}

// constInt evaluates small constant integer expressions used as array
// lengths: literals, unary +/- and the arithmetic/bitwise ops. Idents
// (named constants) and anything else report not-ok.
func constInt(e ast.Expr) (int64, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.INT {
			v, err := strconv.ParseInt(x.Value, 0, 64)
			return v, err == nil
		}
	case *ast.ParenExpr:
		return constInt(x.X)
	case *ast.UnaryExpr:
		v, ok := constInt(x.X)
		if !ok {
			return 0, false
		}
		switch x.Op {
		case token.ADD:
			return v, true
		case token.SUB:
			return -v, true
		case token.XOR:
			return ^v, true
		}
	case *ast.BinaryExpr:
		l, ok1 := constInt(x.X)
		r, ok2 := constInt(x.Y)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch x.Op {
		case token.ADD:
			return l + r, true
		case token.SUB:
			return l - r, true
		case token.MUL:
			return l * r, true
		case token.QUO:
			if r != 0 {
				return l / r, true
			}
		case token.REM:
			if r != 0 {
				return l % r, true
			}
		case token.SHL:
			return l << uint(r), true
		case token.SHR:
			return l >> uint(r), true
		case token.AND:
			return l & r, true
		case token.OR:
			return l | r, true
		case token.XOR:
			return l ^ r, true
		}
	}
	return 0, false
}

// UsedSymbolsOf is [UsedSymbols] for Go code.
func UsedSymbolsOf(f *File) []runtime.SymbolID {
	// Every SelectorExpr whose X is an import's local name counts.
	if f.sf == nil || f.sf.AST == nil {
		return nil
	}
	local := map[string]string{}
	for _, imp := range f.sf.Imports {
		local[imp.LocalName()] = imp.Path
	}
	locals := syntax.NewLocalScopes(f.sf.AST)
	seen := map[runtime.SymbolID]bool{}
	var out []runtime.SymbolID
	ast.Inspect(f.sf.AST, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := se.X.(*ast.Ident)
		if !ok || locals.At(x.Name, x.Pos()) {
			return true
		}
		if path, ok := local[x.Name]; ok {
			sid := runtime.SymbolID{PackagePath: path, Name: se.Sel.Name}
			if !seen[sid] {
				seen[sid] = true
				out = append(out, sid)
			}
		}
		return true
	})
	return out
}

// CommentsOf is [Comments] for Go code.
func CommentsOf(f *File) []*Comment {
	// go/parser collects free comments into ast.File.Comments without
	// attaching them to nodes, so "attached" is computed by walking the
	// AST once for the CommentGroups it reaches.
	if f.sf == nil || f.sf.AST == nil {
		return nil
	}
	attached := map[*ast.CommentGroup]bool{}
	ast.Inspect(f.sf.AST, func(n ast.Node) bool {
		if g, ok := n.(*ast.CommentGroup); ok {
			attached[g] = true
		}
		return true
	})
	var out []*Comment
	for _, g := range f.sf.AST.Comments {
		for _, c := range g.List {
			cv := &Comment{Text: c.Text, Free: !attached[g]}
			if f.fset != nil {
				cv.Pos = posOf(f.fset, c.Pos())
			}
			out = append(out, cv)
		}
	}
	return out
}

// ImportsOf is [Imports] for Go code.
func ImportsOf(f *File) []*Import {
	if f.sf == nil {
		return nil
	}
	refs := map[*syntax.Import]*runtime.ImportRef{}
	if f.pkg != nil {
		if rl := f.pkg.Imports[f.sf]; rl != nil {
			for i, imp := range f.sf.Imports {
				if i < len(rl) && rl[i].Path == imp.Path {
					refs[imp] = rl[i]
				}
			}
		}
	}
	var out []*Import
	for _, imp := range f.sf.Imports {
		out = append(out, NewImport(f.fset, imp, refs[imp]))
	}
	return out
}

// ASTOf is [AST] on a [Decl], for Go code, returning an error where AST
// traps.
func ASTOf(s *Decl) (any, error) {
	// The spec, not the enclosing GenDecl, is the anchor for type, var
	// and const decls: it owns exactly this decl's doc, names and
	// values, while a GenDecl spans every spec in its group.
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.AST: host symbol %s has no declaration", s.Name)
	}
	if s.decl.Func != nil {
		return s.decl.Func, nil
	}
	if s.decl.Spec != nil {
		return s.decl.Spec, nil
	}
	return nil, fmt.Errorf("inspect.AST: %s carries no AST node", s.Name)
}

// Node is one statement, expression or declaration inside a function
// body.
type Node struct {
	Kind   string    // go/ast node name: "BlockStmt", "CallExpr", "Ident", ...
	Text   string    // printer spelling of the node
	Detail string    // the operator or literal kind where the node has one: "+", "STRING", ":="
	Role   string    // the field name it fills in its parent ("List", "Fun", "Cond"); "" on the Body root
	Pos    *Position // position of the node

	node    ast.Node
	file    *syntax.File
	pkg     *runtime.Package
	locals  map[string]bool // names the enclosing func binds — shared by every node under the same Body root
	tparams map[string]bool // type parameter names the enclosing generic func binds — same Body-root sharing
	parent  *Node           // the enclosing node, nil on a root
}

// NewNode returns the [Node] view of n in file f of package p. role is
// the go/ast field name n fills in its parent; pass "" for a body root.
// Unlike nodes from [BodyOf], it does not know the enclosing function's
// local names, so [ExprOf] on it may attribute a shadowed name to a
// package.
func NewNode(n ast.Node, role string, f *syntax.File, p *runtime.Package) *Node {
	v := &Node{Role: role, node: n, file: f, pkg: p}
	if n == nil {
		return v
	}
	v.Kind = nodeKind(n)
	v.Detail = nodeDetail(n)
	if p != nil && p.Fset != nil {
		v.Pos = posOf(p.Fset, n.Pos())
	}
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if p != nil && p.Fset != nil {
		fset = p.Fset
	}
	if err := printer.Fprint(&buf, fset, n); err == nil {
		v.Text = buf.String()
	}
	return v
}

// nodeKind reports the concrete ast node name without the package
// ("*ast.CallExpr" -> "CallExpr"), the same convention TypeExpr.Kind
// follows.
func nodeKind(n ast.Node) string {
	t := reflect.TypeOf(n)
	if t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	return t.Name()
}

// nodeDetail reports the single token that carries the node's
// semantics beyond its Kind — a literal kind (BasicLit) or an
// operator/keyword (binary, unary, assign, inc-dec, branch, range,
// gen-decl). "" everywhere else.
func nodeDetail(n ast.Node) string {
	switch x := n.(type) {
	case *ast.BasicLit:
		return x.Kind.String()
	case *ast.UnaryExpr:
		return x.Op.String()
	case *ast.BinaryExpr:
		return x.Op.String()
	case *ast.AssignStmt:
		return x.Tok.String()
	case *ast.IncDecStmt:
		return x.Tok.String()
	case *ast.BranchStmt:
		return x.Tok.String()
	case *ast.RangeStmt:
		return x.Tok.String()
	case *ast.GenDecl:
		return x.Tok.String()
	}
	return ""
}

// BodyOf is [Body] for Go code, returning an error where Body traps.
func BodyOf(s *Decl) (*Node, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.Body: host symbol %s has no declaration", s.Name)
	}
	if s.decl.Func == nil {
		return nil, fmt.Errorf("inspect.Body: %s is a %s, not a func", s.Name, s.Kind)
	}
	if s.decl.Func.Body == nil {
		return nil, nil
	}
	// The root records the names the func binds, which Expr hands to
	// TypeExpr so a local shadowing an import cannot be misattributed
	// to the package.
	n := NewNode(s.decl.Func.Body, "", s.file, s.Package)
	n.locals = localNames(s.decl.Func)
	n.tparams = declTypeParams(s)
	return n, nil
}

// localNames collects every identifier a func decl binds in its own
// scopes — receiver, params, named results, := names, range and
// type-switch bindings, local var/const/type declarations, and funclit
// params — anywhere in the body.
// The model is deliberately shallow: a name from a sibling or outer
// scope counts too, so membership means "possibly local", never the
// other way. Expr-fed SymbolID refuses package attribution on those
// names, trading under-resolution for never mistaking a method call
// for an import member.
func localNames(fn *ast.FuncDecl) map[string]bool {
	if fn == nil {
		return nil
	}
	names := map[string]bool{}
	add := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				names[n.Name] = true
			}
		}
	}
	add(fn.Recv)
	if fn.Type != nil {
		add(fn.Type.Params)
		add(fn.Type.Results)
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						names[id.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE {
				if id, ok := x.Key.(*ast.Ident); ok {
					names[id.Name] = true
				}
				if id, ok := x.Value.(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
		case *ast.DeclStmt:
			gd, ok := x.Decl.(*ast.GenDecl)
			if !ok {
				break
			}
			for _, spec := range gd.Specs {
				switch sp := spec.(type) {
				case *ast.ValueSpec:
					for _, id := range sp.Names {
						names[id.Name] = true
					}
				case *ast.TypeSpec:
					names[sp.Name.Name] = true
				}
			}
		case *ast.FuncLit:
			if x.Type != nil {
				add(x.Type.Params)
				add(x.Type.Results)
			}
		}
		return true
	})
	return names
}

// NodesOf is [Nodes] for Go code.
func NodesOf(n *Node) []*Node {
	// A role-keeping generalization of ast.Walk's enumeration, computed
	// reflectively so every node type is covered.
	if n == nil || n.node == nil {
		return nil
	}
	rv := reflect.ValueOf(n.node)
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	nodeT := reflect.TypeOf((*ast.Node)(nil)).Elem()
	rt := rv.Type()
	var out []*Node
	for i := 0; i < rt.NumField(); i++ {
		fd := rt.Field(i)
		if fd.PkgPath != "" { // unexported — unreachable through Interface()
			continue
		}
		fv := rv.Field(i)
		if fv.Type().Implements(nodeT) {
			if cn, ok := fv.Interface().(ast.Node); ok && !isNilNode(cn) {
				out = append(out, n.child(cn, fd.Name))
			}
			continue
		}
		if fv.Kind() == reflect.Slice {
			for j := 0; j < fv.Len(); j++ {
				if cn, ok := fv.Index(j).Interface().(ast.Node); ok && !isNilNode(cn) {
					out = append(out, n.child(cn, fd.Name))
				}
			}
		}
	}
	return out
}

// child builds one descendant view in the same context — file,
// package, and the enclosing func's local-name set.
func (n *Node) child(cn ast.Node, role string) *Node {
	c := NewNode(cn, role, n.file, n.pkg)
	c.locals = n.locals
	c.tparams = n.tparams
	c.parent = n
	return c
}

// isNilNode reports whether a node-typed value is absent — a nil
// interface element or a typed nil pointer, both of which appear in
// optional AST fields (Else, Type of a type switch's guard).
func isNilNode(n ast.Node) bool {
	if n == nil {
		return true
	}
	rv := reflect.ValueOf(n)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// ExprOf is [Expr] for Go code.
func ExprOf(n *Node) *TypeExpr {
	if n == nil || n.node == nil {
		return nil
	}
	e, ok := n.node.(ast.Expr)
	if !ok {
		return nil
	}
	te := NewTypeExpr(e, n.file, n.pkg)
	te.locals = n.locals
	te.tparams = n.tparams
	if _, ok := e.(*ast.Ident); ok {
		te.nonRef = !refersByRole(n)
	}
	return te
}

// refersByRole reports whether an identifier node in a body can refer
// to a declaration, judged by the field it fills: a selector's Sel, a
// label, and a declared name (Names, Name) never do. A composite-literal
// key refers only in a map or array literal (map[K]V{k: v}); a struct
// literal's key is a field name, and a key whose literal type cannot be
// told apart is treated as one — under-resolution over misattribution.
func refersByRole(n *Node) bool {
	switch n.Role {
	case "Sel", "Label", "Name", "Names":
		return false
	case "Key":
		kv := n.parent
		if kv == nil || kv.parent == nil {
			return true
		}
		if _, ok := kv.node.(*ast.KeyValueExpr); !ok {
			return true
		}
		if _, ok := kv.parent.node.(*ast.CompositeLit); !ok {
			return true
		}
		return litKeysRefer(kv.parent)
	}
	return true
}

// litKeysRefer reports whether the composite literal at cl takes value
// keys (map or array/slice) rather than field names. The literal's type
// is resolved through local and package type decls, aliases and generic
// instantiations; an elided type comes from the enclosing literal's
// element or key type. Anything unknown — an imported type, a type
// parameter, a name the scope walk cannot pin down — reports false.
func litKeysRefer(cl *Node) bool {
	lt, ok := cl.litTypeOf()
	if !ok {
		return false
	}
	u, ok := cl.litUnderlying(lt)
	if !ok {
		return false
	}
	switch u.t.(type) {
	case *ast.MapType, *ast.ArrayType:
		return true
	}
	return false
}

// litType is a type expression together with the scope its names
// resolve in: the literal's func body at pos (local), or the package
// scope of a package-level spec, whose own type parameters are tparams.
type litType struct {
	t       ast.Expr
	local   bool
	pos     token.Pos
	tparams map[string]bool
}

// litTypeOf returns the type of the composite literal at n. An elided
// type (`{...}` inside `[]map[K]V{...}`, or a map key `{...}`) is the
// enclosing literal's element or key type; an elided `&T` makes the
// literal a T.
func (n *Node) litTypeOf() (litType, bool) {
	x, ok := n.node.(*ast.CompositeLit)
	if !ok {
		return litType{}, false
	}
	if x.Type != nil {
		return litType{t: x.Type, local: true, pos: x.Pos()}, true
	}
	role, outer := n.Role, n.parent
	if outer == nil {
		return litType{}, false
	}
	if _, ok := outer.node.(*ast.KeyValueExpr); ok {
		if role != "Key" && role != "Value" {
			return litType{}, false
		}
		outer = outer.parent
	} else if role != "Elts" {
		return litType{}, false
	}
	if outer == nil {
		return litType{}, false
	}
	ot, ok := outer.litTypeOf()
	if !ok {
		return litType{}, false
	}
	u, ok := outer.litUnderlying(ot)
	if !ok {
		return litType{}, false
	}
	switch t := u.t.(type) {
	case *ast.MapType:
		if role == "Key" {
			u.t = t.Key
		} else {
			u.t = syntax.PeelLitType(t, 1)
		}
	case *ast.ArrayType:
		if role == "Key" {
			return litType{}, false
		}
		u.t = syntax.PeelLitType(t, 1)
	default:
		return litType{}, false
	}
	if st, ok := ast.Unparen(u.t).(*ast.StarExpr); ok {
		u.t = st.X
	}
	return u, true
}

// litUnderlying resolves a named literal type to the type expression
// its declaration spells, following local `type` decls in scope at the
// literal, package-level decls, aliases and generic instantiations
// (whose shape is the generic decl's). ok is false where the name
// cannot be pinned down: an imported type, a type parameter, or a
// local name with no `type` decl in scope. A predeclared or unknown
// name comes back as itself.
func (n *Node) litUnderlying(lt litType) (litType, bool) {
	for fuel := 8; fuel > 0; fuel-- {
		t := ast.Unparen(lt.t)
		switch x := t.(type) {
		case *ast.IndexExpr:
			t = ast.Unparen(x.X)
		case *ast.IndexListExpr:
			t = ast.Unparen(x.X)
		}
		var id *ast.Ident
		switch x := t.(type) {
		case *ast.Ident:
			id = x
		case *ast.SelectorExpr:
			return litType{}, false
		default:
			lt.t = t
			return lt, true
		}
		if lt.tparams[id.Name] {
			return litType{}, false
		}
		if lt.local {
			if n.tparams[id.Name] {
				return litType{}, false
			}
			if n.locals[id.Name] {
				ts := n.localTypeSpec(id.Name, lt.pos)
				if ts == nil {
					return litType{}, false
				}
				lt = litType{t: ts.Type, local: true, pos: ts.Name.Pos()}
				continue
			}
		}
		ts := n.pkgTypeSpec(id.Name)
		if ts == nil {
			lt.t = id
			return lt, true
		}
		lt = litType{t: ts.Type, tparams: typeParamNames(ts.TypeParams)}
	}
	return litType{}, false
}

// localTypeSpec finds the `type name` decl in scope at pos, walking the
// statement lists of n's enclosing blocks innermost first.
func (n *Node) localTypeSpec(name string, pos token.Pos) *ast.TypeSpec {
	for a := n; a != nil; a = a.parent {
		var list []ast.Stmt
		switch s := a.node.(type) {
		case *ast.BlockStmt:
			list = s.List
		case *ast.CaseClause:
			list = s.Body
		case *ast.CommClause:
			list = s.Body
		}
		var found *ast.TypeSpec
		for _, st := range list {
			for {
				ls, ok := st.(*ast.LabeledStmt)
				if !ok {
					break
				}
				st = ls.Stmt
			}
			ds, ok := st.(*ast.DeclStmt)
			if !ok {
				continue
			}
			gd, ok := ds.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok && ts.Name.Name == name && ts.Name.Pos() < pos {
					found = ts
				}
			}
		}
		if found != nil {
			return found
		}
	}
	return nil
}

// pkgTypeSpec returns the package-level `type name` decl of n's package.
func (n *Node) pkgTypeSpec(name string) *ast.TypeSpec {
	if n.pkg == nil || n.pkg.Index == nil {
		return nil
	}
	td, ok := n.pkg.Index.Types[name]
	if !ok || td.Decl == nil {
		return nil
	}
	ts, _ := td.Decl.Spec.(*ast.TypeSpec)
	return ts
}

// ASTNode returns the underlying go/ast node.
func (n *Node) ASTNode() ast.Node { return n.node }

// SyntaxFile returns the parsed file behind the view.
func (f *File) SyntaxFile() *syntax.File { return f.sf }

// Pkg returns the package the file belongs to.
func (f *File) Pkg() *runtime.Package { return f.pkg }

// DeclOf returns the index entry behind the view, or nil for a host
// decl.
func (s *Decl) DeclOf() *index.Decl { return s.decl }

// DeclFile returns the parsed declaring file, or nil for a host decl.
func (s *Decl) DeclFile() *syntax.File { return s.file }

// Target returns the Go type of a host decl, or nil when unknown or for
// a source decl.
func (s *Decl) Target() reflect.Type { return s.htype }
