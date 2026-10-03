// Package inspect is the minigo introspection stub package
// (docs/sketch/plan-package-introspection.md): a real, importable Go
// package whose function declarations name the engine's inspect
// intrinsics. Scripts import it as "minigo.dev/inspect" or by its
// module path — the engine binds both to the same intrinsics, so the
// panic bodies in stub.go never execute inside minigo.
//
// The view types (Decl, File, Import, Field, Sig, TypeExpr) are real:
// intrinsics return them boxed as host values, so field access works
// through reflective member dispatch. Unexported fields keep the AST
// context (declaring file, expr) that functions like SymbolID and
// UnWrap resolve against — they are intentionally out of the FFI.
package inspect

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"reflect"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// BuiltinPackagePath is the pseudo import path predeclared identifiers
// resolve to.
const BuiltinPackagePath = ":builtin:"

// Decl is a declaration-level view of one package member (the "Symbol"
// the script API speaks of; renamed here to avoid colliding with the
// Symbol stub function).
type Decl struct {
	// owning package — SymbolOf(x).Package chains. Member access on it
	// resolves the package namespace only (pkg.Foo is the decl); read
	// its metadata through the inspect.* accessors (Path, Name, Dir,
	// State, Standard) instead.
	Package *runtime.Package
	Kind    string // "func"|"method"|"var"|"const"|"type"|"host"
	Name    string
	File    string // declaring file name ("" for host symbols)
	Pos     string // "file.go:12:6" ("" for host symbols)
	Doc     string // doc comment text ("" for host symbols)

	decl  *index.Decl
	file  *syntax.File
	hsig  *Sig // synthesized host signature (BuiltinFunc.Target)
	htype reflect.Type
}

// NewDecl builds a source-decl view.
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
		s.Pos = pkg.Fset.Position(d.Pos).String()
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
			s.Doc = ts.Doc.Text()
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
			s.Doc = vs.Doc.Text()
		} else {
			s.Doc = docText(d.Gen.Doc)
		}
	default:
		s.Kind = "decl"
	}
	return s
}

// NewHostDecl builds a Kind:"host" pseudo-decl for a bound member.
// sig may be nil.
func NewHostDecl(pkg *runtime.Package, name string, sig *Sig, target reflect.Type) *Decl {
	return &Decl{Package: pkg, Kind: "host", Name: name, hsig: sig, htype: target}
}

// File is a view of one source file of a package.
type File struct {
	Name string
	Doc  string

	pkg  *runtime.Package
	sf   *syntax.File
	fset *token.FileSet
}

// NewFile builds a file view.
func NewFile(pkg *runtime.Package, sf *syntax.File) *File {
	f := &File{pkg: pkg, sf: sf, fset: pkg.Fset}
	f.Name = sf.Name
	if sf.AST != nil && sf.AST.Doc != nil {
		f.Doc = sf.AST.Doc.Text()
	}
	return f
}

// Import is one entry of a file's import table.
type Import struct {
	Path string
	Name string // local name: alias or the package's declared name
	Pos  string

	ref *runtime.ImportRef // materializes on demand
}

// NewImport builds an import view for one syntax import entry.
func NewImport(fset *token.FileSet, imp *syntax.Import, ref *runtime.ImportRef) *Import {
	i := &Import{Path: imp.Path, Name: imp.LocalName(), ref: ref}
	if fset != nil {
		i.Pos = fset.Position(imp.Pos).String()
	}
	return i
}

// Ref exposes the backing ImportRef — engine-only.
func (i *Import) Ref() *runtime.ImportRef { return i.ref }

// Field is a view over one *ast.Field: a struct field, a signature
// parameter, or a method's receiver.
type Field struct {
	Names    []string // empty for embedded fields and unnamed params
	Type     *TypeExpr
	Tag      string // struct tag, unquoted
	Doc      string
	Embedded bool
	Pos      string
}

// Sig is a func/method declaration's shape (the "Signature" the script
// API speaks of; renamed to avoid colliding with the Signature stub).
type Sig struct {
	Recv    *Field // nil for plain funcs
	Params  *runtime.Slice
	Results *runtime.Slice
}

// ParamFields returns the parameters as Field views — the host-side
// counterpart of FieldsOf (Params stays boxed for script member
// dispatch).
func (s *Sig) ParamFields() []*Field { return unboxFields(s.Params) }

// ResultFields returns the results as Field views.
func (s *Sig) ResultFields() []*Field { return unboxFields(s.Results) }

// TypeExpr is a handle over one type expression: the declared spelling
// plus the context needed to resolve the names it mentions.
type TypeExpr struct {
	Text string // printer spelling of the declaration
	Kind string // ast node name: "Ident", "SelectorExpr", "StarExpr", ...

	expr ast.Expr
	file *syntax.File
	pkg  *runtime.Package
	ht   reflect.Type // host-backed alternative (BuiltinFunc.Target sigs)
}

// NewTypeExpr builds a syntax-backed type expression.
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

// NewHostType builds a reflect-backed type expression for host symbols.
func NewHostType(t reflect.Type) *TypeExpr {
	te := &TypeExpr{ht: t}
	if t != nil {
		te.Kind = "reflect:" + t.Kind().String()
		te.Text = t.String()
	}
	return te
}

// Resolver turns a SymbolID into the decl view it names — the engine
// supplies the implementation.
type Resolver func(sid runtime.SymbolID) (*Decl, error)

func docText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return g.Text()
}

func fieldList(fl *ast.FieldList, f *syntax.File, p *runtime.Package) []*Field {
	if fl == nil {
		return nil
	}
	var out []*Field
	for _, fd := range fl.List {
		fv := &Field{Type: NewTypeExpr(fd.Type, f, p), Doc: fieldDoc(fd)}
		for _, n := range fd.Names {
			fv.Names = append(fv.Names, n.Name)
		}
		fv.Embedded = len(fd.Names) == 0
		if fd.Tag != nil {
			fv.Tag = strings.Trim(fd.Tag.Value, "`")
		}
		if p != nil && p.Fset != nil {
			fv.Pos = p.Fset.Position(fd.Pos()).String()
		}
		out = append(out, fv)
	}
	return out
}

func fieldDoc(fd *ast.Field) string {
	if fd.Doc != nil {
		return fd.Doc.Text()
	}
	if fd.Comment != nil {
		return fd.Comment.Text()
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

// FieldsOf returns the declared fields of a struct type symbol, or the
// member elements of an interface type symbol: method specs carry their
// names and the signature as a FuncType TypeExpr, while embedded and
// constraint elements (~T, unions) come back Embedded with the element
// expression as Type. Other symbols report an error — call Def and
// navigate the TypeExpr when the spelling matters.
func FieldsOf(s *Decl) ([]*Field, error) {
	if s.decl == nil {
		return nil, fmt.Errorf("inspect.Fields: host symbol %s has no declaration", s.Name)
	}
	ts, ok := s.decl.Spec.(*ast.TypeSpec)
	if !ok {
		return nil, fmt.Errorf("inspect.Fields: %s is not a type", s.Name)
	}
	switch t := ts.Type.(type) {
	case *ast.StructType:
		return fieldList(t.Fields, s.file, s.Package), nil
	case *ast.InterfaceType:
		return fieldList(t.Methods, s.file, s.Package), nil
	}
	return nil, fmt.Errorf("inspect.Fields: %s is not a struct or interface type", s.Name)
}

// MReqsOf returns the named member requirements of an interface type
// symbol — the method specs, each Field carrying its name and signature
// (a FuncType TypeExpr). Embedded and constraint elements are skipped;
// IEmbeds covers them. Non-interface symbols report an error.
func MReqsOf(s *Decl) ([]*Field, error) {
	it, err := ifaceOf("MReqs", s)
	if err != nil {
		return nil, err
	}
	var out []*Field
	for _, fv := range fieldList(it.Methods, s.file, s.Package) {
		if !fv.Embedded {
			out = append(out, fv)
		}
	}
	return out, nil
}

// IEmbedsOf returns the embedded elements of an interface type symbol —
// embedded interface names and constraint elements (~T, union
// expressions) as their written TypeExprs. Non-interface symbols
// report an error.
func IEmbedsOf(s *Decl) ([]*TypeExpr, error) {
	it, err := ifaceOf("IEmbeds", s)
	if err != nil {
		return nil, err
	}
	var out []*TypeExpr
	for _, fv := range fieldList(it.Methods, s.file, s.Package) {
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
		return nil, fmt.Errorf("inspect.%s: %s is not an interface type", op, s.Name)
	}
	return it, nil
}

// MethodsOf returns the method decls of a type symbol.
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
	return out, nil
}

// Method is a member of a type's method set: a method declared on the
// type (Decl set, Via nil) or one promoted through an embedded field
// (Via names the decl the member was promoted from). Members promoted
// out of an embedded interface are method specs — they carry Name and
// Sig but no Decl, since a spec is not a declaration.
type Method struct {
	Name string
	Sig  *Sig
	Decl *Decl // nil when the member is an interface method spec
	Via  *Decl // nil when declared on the type itself
}

// MethodSetOf returns the flattened method set of a type symbol: its
// declared methods plus the members promoted through embedded fields,
// walked transitively. Promotion follows Go's value method-set rule —
// a by-value embed (struct{ T }) lifts T's non-pointer-receiver
// members, a pointer embed (struct{ *T }) and interface embeds lift
// everything. Alias and generic-instantiation embeds resolve to the
// underlying named decl first; type parameters and unresolvable embeds
// contribute nothing. A shallower promotion shadows a deeper one by
// name; declared members always win.
func MethodSetOf(s *Decl, res Resolver) ([]*Method, error) {
	if s.decl == nil || s.Package == nil || s.Package.Index == nil {
		return nil, fmt.Errorf("inspect.MethodSet: %s has no index", s.Name)
	}
	td, ok := s.Package.Index.Types[s.Name]
	if !ok {
		return nil, fmt.Errorf("inspect.MethodSet: %s is not a type", s.Name)
	}
	seen := map[string]bool{}
	visited := map[runtime.SymbolID]bool{}
	var out []*Method
	var walk func(td *index.TypeDeclInfo, owner *Decl, via *Decl, ptrEmbed bool)
	walk = func(td *index.TypeDeclInfo, owner *Decl, via *Decl, ptrEmbed bool) {
		for _, md := range td.Methods {
			if seen[md.Name] {
				continue
			}
			m := NewDecl(owner.Package, md)
			sig, err := SignatureOf(m)
			if err != nil {
				continue
			}
			if !ptrEmbed && via != nil && sig.Recv != nil && sig.Recv.Type.Kind == "StarExpr" {
				continue // value method sets skip pointer receivers
			}
			seen[md.Name] = true
			out = append(out, &Method{Name: md.Name, Sig: sig, Decl: m, Via: via})
		}
		fs, err := FieldsOf(owner)
		if err != nil {
			return // not a struct or interface — nothing to promote from
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
			var ed *Decl
			for i := 0; i < 8; i++ {
				sid, ok := sub.SymbolID()
				if !ok {
					if kids := sub.Children(); len(kids) > 0 {
						sub = kids[0]
						continue
					}
					break
				}
				if sid.PackagePath == BuiltinPackagePath || visited[sid] {
					break
				}
				visited[sid] = true
				d, err := res(sid)
				if err != nil || d == nil || d.decl == nil {
					break
				}
				ts, ok := d.decl.Spec.(*ast.TypeSpec)
				if !ok {
					break
				}
				ed = d
				switch ts.Type.(type) {
				case *ast.Ident, *ast.SelectorExpr:
					sub = NewTypeExpr(ts.Type, d.file, d.Package)
					continue // another named layer — keep chasing
				}
				break
			}
			if ed == nil {
				continue
			}
			if it, ok := ed.decl.Spec.(*ast.TypeSpec).Type.(*ast.InterfaceType); ok {
				// embedded interfaces promote their method specs —
				// recursively including the specs' own embeds.
				promoteIfaceSpecs(it, ed, res, seen, visited, &out)
				continue
			}
			if ntd, ok := ed.Package.Index.Types[ed.Name]; ok {
				walk(ntd, ed, ed, byPtr)
			}
		}
	}
	if it, ok := s.decl.Spec.(*ast.TypeSpec).Type.(*ast.InterfaceType); ok {
		promoteIfaceSpecs(it, s, res, seen, visited, &out)
	}
	walk(td, s, nil, false)
	return out, nil
}

// promoteIfaceSpecs lists an interface's members as method-set
// entries: named specs become spec-backed Methods (no Decl); embedded
// elements recurse so the promoted set is transitive.
func promoteIfaceSpecs(it *ast.InterfaceType, owner *Decl, res Resolver, seen map[string]bool, visited map[runtime.SymbolID]bool, out *[]*Method) {
	for _, fd := range fieldList(it.Methods, owner.file, owner.Package) {
		if !fd.Embedded {
			if seen[fd.Names[0]] {
				continue
			}
			seen[fd.Names[0]] = true
			ft, _ := fd.Type.Expr().(*ast.FuncType)
			*out = append(*out, &Method{
				Name: fd.Names[0],
				Sig: &Sig{
					Params:  boxFields(fieldList(ft.Params, owner.file, owner.Package)),
					Results: boxFields(fieldList(ft.Results, owner.file, owner.Package)),
				},
				Via: owner,
			})
			continue
		}
		// an embedded element promotes the elements it names.
		sid, ok := fd.Type.SymbolID()
		if !ok || sid.PackagePath == BuiltinPackagePath || visited[sid] {
			continue
		}
		visited[sid] = true
		d, err := res(sid)
		if err != nil || d == nil || d.decl == nil {
			continue
		}
		ts, ok := d.decl.Spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		if sub, ok := ts.Type.(*ast.InterfaceType); ok {
			promoteIfaceSpecs(sub, d, res, seen, visited, out)
		}
	}
}

// EnumMembersOf returns a type symbol's enum members: the package's
// const declarations that are explicitly typed with it, in source
// order. Untyped constants and foreign-typed ones never match, so an
// empty slice means the type is not an enum. Non-type symbols report
// an error.
func EnumMembersOf(s *Decl) ([]*Decl, error) {
	if s.decl == nil || s.Package == nil || s.Package.Index == nil {
		return nil, fmt.Errorf("inspect.EnumMembers: %s has no index", s.Name)
	}
	if s.decl.Kind != index.TypeDecl {
		return nil, fmt.Errorf("inspect.EnumMembers: %s is a %s, not a type", s.Name, s.Kind)
	}
	var out []*Decl
	for _, cd := range s.Package.Index.Decls {
		if cd.Kind != index.ConstDecl {
			continue
		}
		t := valueSpecType(cd)
		if t == nil {
			continue
		}
		sid, ok := NewTypeExpr(t, cd.File, s.Package).SymbolID()
		if !ok || sid.PackagePath != s.Package.Path || sid.Name != s.Name {
			continue
		}
		out = append(out, NewDecl(s.Package, cd))
	}
	return out, nil
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

// SignatureOf returns the func/method signature, or the synthesized
// host signature for a bound intrinsic that carries a Target.
func SignatureOf(s *Decl) (*Sig, error) {
	if s.hsig != nil {
		return s.hsig, nil
	}
	if s.decl == nil || s.decl.Func == nil {
		return nil, fmt.Errorf("inspect.Signature: %s has no signature", s.Name)
	}
	ft := s.decl.Func.Type
	sig := &Sig{
		Params:  boxFields(fieldList(ft.Params, s.file, s.Package)),
		Results: boxFields(fieldList(ft.Results, s.file, s.Package)),
	}
	if s.decl.Func.Recv != nil && len(s.decl.Func.Recv.List) > 0 {
		recv := s.decl.Func.Recv.List[0]
		fv := &Field{Type: NewTypeExpr(recv.Type, s.file, s.Package)}
		for _, n := range recv.Names {
			fv.Names = append(fv.Names, n.Name)
		}
		if s.Package != nil && s.Package.Fset != nil {
			fv.Pos = s.Package.Fset.Position(recv.Pos()).String()
		}
		sig.Recv = fv
	}
	return sig, nil
}

// TypeParamsOf returns the type parameter fields of a generic decl.
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
	return fieldList(fl, s.file, s.Package), nil
}

// DefOf returns the declared type expression of a type symbol.
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
	return NewTypeExpr(ts.Type, s.file, s.Package), nil
}

// DeclTypeOf returns the type expression declared on a var or const
// decl — the explicit annotation (`const X Status = ...`, `var x
// Status`), or the type an empty const spec inherits (`B` under `A
// Status = e`). Untyped value specs report nil; other decl kinds and
// host symbols report an error.
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

// IsAliasOf reports whether a type decl spells an alias declaration
// (`type X = int`) rather than a defined type (`type X int`) — the `=`
// in the spec is the only difference, so the two forms partition type
// decls: every source type symbol is exactly one. An alias denotes its
// target rather than declaring a type of its own, so codegen consumers
// skip it for directives and method generation. The distinction is
// orthogonal to enum-ness — a const may still be typed with the alias
// (EnumMembers lists it) — and to the underlying shape (Def reads it).
// Non-type symbols report an error.
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

// Expr exposes the underlying ast.Expr — engine-only, out of the FFI
// (member dispatch sees only exported fields).
func (te *TypeExpr) Expr() ast.Expr { return te.expr }

// Children drills into a composite type expression: []T -> T,
// map[K]V -> K then V, *T -> T, func(A) B -> A then B,
// F[A] -> F then A (the generic's base leads the arguments).
func (te *TypeExpr) Children() []*TypeExpr {
	if te.ht != nil {
		return te.hostChildren()
	}
	wrap := func(e ast.Expr) *TypeExpr { return NewTypeExpr(e, te.file, te.pkg) }
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
		for _, fd := range fieldList(e.Params, te.file, te.pkg) {
			out = append(out, fd.Type)
		}
		for _, fd := range fieldList(e.Results, te.file, te.pkg) {
			out = append(out, fd.Type)
		}
	case *ast.StructType:
		for _, fd := range fieldList(e.Fields, te.file, te.pkg) {
			out = append(out, fd.Type)
		}
	case *ast.InterfaceType:
		for _, fd := range fieldList(e.Methods, te.file, te.pkg) {
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

// TypeFieldsOf returns the member elements of a composite type
// expression: a struct spelling yields its fields (names, type, tag),
// an interface spelling yields its elements (method specs named with a
// FuncType TypeExpr; embedded and constraint elements Embedded). This
// is the TypeExpr-level counterpart of FieldsOf — anonymous composite
// types inside a decl's fields become readable without naming the
// decl. Other shapes report an error.
func TypeFieldsOf(te *TypeExpr) ([]*Field, error) {
	if te.ht != nil {
		return hostTypeFields(te.ht)
	}
	switch e := te.expr.(type) {
	case *ast.StructType:
		return fieldList(e.Fields, te.file, te.pkg), nil
	case *ast.InterfaceType:
		return fieldList(e.Methods, te.file, te.pkg), nil
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

// Unref strips one pointer layer: *T -> T; other shapes pass through.
// (Named Unref — UnRef is taken by the stub declaration.)
func (te *TypeExpr) Unref() *TypeExpr {
	if te.ht != nil {
		if te.ht.Kind() == reflect.Pointer {
			return NewHostType(te.ht.Elem())
		}
		return te
	}
	if e, ok := te.expr.(*ast.StarExpr); ok {
		return NewTypeExpr(e.X, te.file, te.pkg)
	}
	return te
}

// Unwrap peels one declared-type layer: an identifier or selector naming
// a type resolves to that decl's underlying TypeExpr; other shapes pass
// through. A newtype and an alias both count as one layer.
func (te *TypeExpr) Unwrap(res Resolver) *TypeExpr {
	sid, ok := te.SymbolID()
	if !ok || sid.PackagePath == BuiltinPackagePath {
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
	return NewTypeExpr(ts.Type, sym.file, sym.Package)
}

// Origin chases the whole declared chain: pointers AND type transitions
// until the terminal base expression, where Origin(x) == x.
// A recursive decl (type Node *Node) stops at the repeated symbol
// rather than looping forever.
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

var predeclared = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"any": true, "comparable": true,
}

// SymbolID resolves a type expression to the SymbolID it names:
// Ident -> declaring package or ":builtin:", SelectorExpr -> the
// declaring file's import table. Composite exprs report not-ok.
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
		if te.pkg != nil && te.pkg.Index != nil {
			if _, ok := te.pkg.Index.Types[e.Name]; ok {
				return runtime.SymbolID{PackagePath: te.pkg.Path, Name: e.Name}, true
			}
		}
		if predeclared[e.Name] {
			return runtime.SymbolID{PackagePath: BuiltinPackagePath, Name: e.Name}, true
		}
		if te.pkg != nil {
			return runtime.SymbolID{PackagePath: te.pkg.Path, Name: e.Name}, true
		}
		return runtime.SymbolID{PackagePath: BuiltinPackagePath, Name: e.Name}, true
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok || te.file == nil {
			return runtime.SymbolID{}, false
		}
		for _, imp := range te.file.Imports {
			if imp.LocalName() == x.Name {
				return runtime.SymbolID{PackagePath: imp.Path, Name: e.Sel.Name}, true
			}
		}
		return runtime.SymbolID{}, false
	}
	return runtime.SymbolID{}, false
}

// CanonicalName renders the type's canonical identity: the fully
// qualified "import/path.Name" for named types (the written spelling's
// local alias is replaced by the declaring file's import path, so the
// same type read through two files compares equal), the plain name for
// builtins, and "*" + the element name for pointer types. Composite
// shapes (slices, maps, func types, ...) report "" — they carry no
// package-qualified identity a consumer could name.
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
		if sid.PackagePath == BuiltinPackagePath {
			return sid.Name
		}
		return sid.PackagePath + "." + sid.Name
	}
}

// SameType is strict structural equality over TypeExpr trees: named
// references compare by SymbolID (declared-type identity — an alias and
// its target do NOT collapse), composites compare Kind and children.
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

// Sub views an arbitrary sub-expression of this type in the same
// file/package context — the parts Children() does not reach, such as
// the base X of an IndexExpr/IndexListExpr, an ArrayType length, or
// anything Expr() exposes. Returns nil for a nil expression.
func (te *TypeExpr) Sub(e ast.Expr) *TypeExpr {
	if e == nil {
		return nil
	}
	return NewTypeExpr(e, te.file, te.pkg)
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
	if !ok || sid.PackagePath == BuiltinPackagePath {
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

// UsedSymbolsOf walks a file's AST for SelectorExpr on an import-local
// name: every imported member the file actually references.
func UsedSymbolsOf(f *File) []runtime.SymbolID {
	if f.sf == nil || f.sf.AST == nil {
		return nil
	}
	local := map[string]string{}
	for _, imp := range f.sf.Imports {
		local[imp.LocalName()] = imp.Path
	}
	seen := map[runtime.SymbolID]bool{}
	var out []runtime.SymbolID
	ast.Inspect(f.sf.AST, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := se.X.(*ast.Ident)
		if !ok {
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

// ImportsOf returns the file's own import table.
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

// SyntaxFile exposes the underlying file — engine-only.
func (f *File) SyntaxFile() *syntax.File { return f.sf }

// Pkg exposes the owning package — engine-only.
func (f *File) Pkg() *runtime.Package { return f.pkg }

// DeclOf exposes the underlying index decl — engine-only.
func (s *Decl) DeclOf() *index.Decl { return s.decl }

// DeclFile exposes the declaring file — engine-only.
func (s *Decl) DeclFile() *syntax.File { return s.file }

// Target exposes the host reflect type — engine-only.
func (s *Decl) Target() reflect.Type { return s.htype }
