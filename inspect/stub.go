package inspect

import "github.com/podhmo/minigo/runtime"

// Script-facing API. The engine binds "minigo.dev/inspect" and this
// package's module path to intrinsics — these bodies never execute
// inside minigo; they exist so scripts stay gopls/gofmt friendly.
// Views are returned boxed, so their fields (s.Name, f.Type.Text, ...)
// work in scripts without further declarations.

// PackageOf loads a package by import path (bound or source).
//
// Member access on the returned *runtime.Package resolves the package
// namespace only — p.Foo is the decl, and a member named like a
// metadata field shadows it. Read metadata through the accessors
// (Path, Name, Dir, State, Standard) instead; p.Path alone traps.
func PackageOf(path string) *runtime.Package { panic("minigo intrinsic") }

// SourceOf loads the source package behind an import path, bypassing a
// bound shadow — stdlib sources stay inspectable where PackageOf would
// return the bound object (a bound package has no index; its Decls are
// host pseudo-decls). For an unbound path it is exactly PackageOf.
// Value-layer access on real GOROOT source may still trap on constructs
// the interpreter does not cover; the index and syntax layers are the
// intended use.
func SourceOf(path string) *runtime.Package { panic("minigo intrinsic") }

// DirOf loads a package by directory.
func DirOf(dir string) *runtime.Package { panic("minigo intrinsic") }

// FileOf loads a single file as a one-file package.
func FileOf(file string) *runtime.Package { panic("minigo intrinsic") }

// Current returns the caller's package.
func Current() *runtime.Package { panic("minigo intrinsic") }

// OwnerOf returns the package a value's symbol was declared in.
func OwnerOf(v any) *runtime.Package { panic("minigo intrinsic") }

// PathOf returns the import path owning a value's symbol.
func PathOf(v any) string { panic("minigo intrinsic") }

// SymbolIDOf returns the canonical {PackagePath, Name} of a value's symbol.
func SymbolIDOf(v any) runtime.SymbolID { panic("minigo intrinsic") }

// SymbolOf returns the declaration view of a value's symbol.
func SymbolOf(v any) *Decl { panic("minigo intrinsic") }

// Decls lists a package's (or one file's) top-level declarations.
func Decls(x any) []*Decl { panic("minigo intrinsic") }

// Symbol looks up one declaration by name.
func Symbol(p *runtime.Package, name string) *Decl { panic("minigo intrinsic") }

// Files lists a package's source files.
func Files(p *runtime.Package) []*File { panic("minigo intrinsic") }

// Imports lists a file's own import table.
func Imports(f *File) []*Import { panic("minigo intrinsic") }

// Kind reports a symbol's kind: func|method|var|const|type|host.
func Kind(s *Decl) string { panic("minigo intrinsic") }

// Doc returns a symbol's doc comment text.
func Doc(s *Decl) string { panic("minigo intrinsic") }

// Pos returns a symbol's "file.go:line:col" position.
func Pos(s *Decl) string { panic("minigo intrinsic") }

// Fields returns the declared fields of a struct type symbol, or the
// member elements of an interface type symbol (named method specs and
// embedded/constraint elements).
func Fields(s *Decl) []*Field { panic("minigo intrinsic") }

// Methods returns the method decls of a type symbol.
func Methods(s *Decl) []*Decl { panic("minigo intrinsic") }

// MReqs returns the named member requirements of an interface type
// symbol — the method specs; embedded/constraint elements are skipped.
func MReqs(s *Decl) []*Field { panic("minigo intrinsic") }

// IEmbeds returns the embedded elements of an interface type symbol —
// embedded interfaces and constraint elements (~T, unions).
func IEmbeds(s *Decl) []*TypeExpr { panic("minigo intrinsic") }

// Signature returns a func/method's {Recv, Params, Results}.
func Signature(s *Decl) *Sig { panic("minigo intrinsic") }

// TypeParams returns a generic decl's type parameter fields.
func TypeParams(s *Decl) []*Field { panic("minigo intrinsic") }

// Def returns a type symbol's declared TypeExpr.
func Def(s *Decl) *TypeExpr { panic("minigo intrinsic") }

// Children drills into a composite type expression.
func Children(te *TypeExpr) []*TypeExpr { panic("minigo intrinsic") }

// UnWrap peels one declared-type layer.
func UnWrap(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// UnRef strips one pointer layer.
func UnRef(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// Origin chases pointers and type transitions to the base expression.
func Origin(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// SymbolID resolves a type expression to the SymbolID it names.
func SymbolID(te *TypeExpr) runtime.SymbolID { panic("minigo intrinsic") }

// Resolve follows a type expression to its declaration view.
func Resolve(te *TypeExpr) *Decl { panic("minigo intrinsic") }

// UsedSymbols lists every imported member a file references.
func UsedSymbols(f *File) []runtime.SymbolID { panic("minigo intrinsic") }

// SameType reports strict structural equality over type expressions.
func SameType(a, b *TypeExpr) bool { panic("minigo intrinsic") }

// Ops returns the lifted op-dataflow view of a function/method decl's
// body — the compiled VM code as a flat op list with explicit value ids
// for tracking call arguments and return values through helpers.
// Non-func decls and host (bound-package) pseudo-decls return nil, so
// scripts descend into user code and stop at stdlib by construction.
func Ops(s *Decl) *Body { panic("minigo intrinsic") }

// Value materializes a package member's runtime value (may run init).
func Value(p *runtime.Package, name string) any { panic("minigo intrinsic") }

// TypeOf materializes a type symbol's *runtime.TypeDef.
func TypeOf(s *Decl) any { panic("minigo intrinsic") }

// Name returns a package's declared name.
func Name(p *runtime.Package) string { panic("minigo intrinsic") }

// Path returns a package's import path.
func Path(p *runtime.Package) string { panic("minigo intrinsic") }

// Dir returns a package's directory.
func Dir(p *runtime.Package) string { panic("minigo intrinsic") }

// State returns a package's load state.
func State(p *runtime.Package) string { panic("minigo intrinsic") }

// Standard reports whether a package is inside GOROOT.
func Standard(p *runtime.Package) bool { panic("minigo intrinsic") }
