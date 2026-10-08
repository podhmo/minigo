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

// Implementers returns the type decls of p whose method set covers
// iface's requirements — its named specs plus everything embedded
// interfaces pull in transitively — the index-level subtype question
// for one package (walking the import closure for the full picture
// stays the caller's job). The method set answers "usable through *T":
// a pointer-receiver method declared on the type itself still counts.
// Interface decls count: an interface embedding the required specs
// satisfies them, and iface itself is included — filter by
// Def(d).Kind for concrete types only. iface must be an interface
// type decl; a constraint interface (~T terms, unions, embedded
// non-interface types) traps — no value type implements it — and
// other shapes trap.
func Implementers(p *runtime.Package, iface *Decl) []*Decl { panic("minigo intrinsic") }

// Files lists a package's source files.
func Files(p *runtime.Package) []*File { panic("minigo intrinsic") }

// Imports lists a file's own import table.
func Imports(f *File) []*Import { panic("minigo intrinsic") }

// Kind reports a symbol's kind: func|method|var|const|type|host.
func Kind(s *Decl) string { panic("minigo intrinsic") }

// Doc returns a symbol's doc comment text.
func Doc(s *Decl) string { panic("minigo intrinsic") }

// Pos returns a decl's declaring position — nil for host symbols.
// The *Position view carries File/Line/Column fields so scripts
// never split "file:line:col" text.
func Pos(s *Decl) *Position { panic("minigo intrinsic") }

// Fields returns the declared fields of a struct type symbol, or the
// member elements of an interface type symbol (named method specs and
// embedded/constraint elements).
func Fields(s *Decl) []*Field { panic("minigo intrinsic") }

// Methods returns the method decls of a type symbol.
func Methods(s *Decl) []*Decl { panic("minigo intrinsic") }

// MethodSet returns the flattened method set of a type symbol — the
// members "usable through *T": the type's declared methods with either
// receiver (a pointer-receiver method declared on T counts, though
// Go's value method set of T would not contain it) plus members
// promoted through embedded fields, walked transitively. Each member
// carries Name and Sig; a promoted member's Via names the decl it was
// promoted from, and its Decl is the underlying method decl (nil for
// interface method specs, which are not declarations). Promotion
// follows the value method-set rule: struct{ T } lifts T's
// non-pointer-receiver members, struct{ *T } and interface embeds lift
// all, and a pointer embed on the path down keeps deeper pointer
// receivers visible. Shallower spellings shadow deeper ones by name;
// a same-depth conflict between distinct members is an ambiguous
// selector and drops out entirely (Go's rule), while declared
// members always win. The list is sorted by name. Non-type symbols
// trap.
func MethodSet(s *Decl) []*Method { panic("minigo intrinsic") }

// EnumMembers returns a type symbol's enum members: the package's
// const declarations that are explicitly typed with it, in source
// order — `const X Status = ...` specs, including empty specs that
// inherit the type (`B` under `A Status = e`). Untyped constants and
// foreign-typed ones never list, so an empty result means the type is
// not an enum. Non-type symbols trap.
func EnumMembers(s *Decl) []*Decl { panic("minigo intrinsic") }

// MReqs returns the named member requirements of an interface type
// symbol — the method specs; embedded/constraint elements are skipped.
// Non-interface type decls return nil; non-type decls trap.
func MReqs(s *Decl) []*Field { panic("minigo intrinsic") }

// IEmbeds returns the embedded elements of an interface type symbol —
// embedded interfaces and constraint elements (~T, unions).
// Non-interface type decls return nil; non-type decls trap.
func IEmbeds(s *Decl) []*TypeExpr { panic("minigo intrinsic") }

// Signature returns a func/method's {Recv, Params, Results}.
func Signature(s *Decl) *Sig { panic("minigo intrinsic") }

// TypeParams returns a generic decl's type parameter fields.
func TypeParams(s *Decl) []*Field { panic("minigo intrinsic") }

// Def returns a type symbol's declared TypeExpr.
func Def(s *Decl) *TypeExpr { panic("minigo intrinsic") }

// DeclType returns the type expression declared on a var or const
// decl — the explicit annotation (`const X Status = ...`, `var x
// Status`), or the type an empty const spec inherits (`B` under `A
// Status = e`). Untyped value specs report nil; other decl kinds trap.
func DeclType(s *Decl) *TypeExpr { panic("minigo intrinsic") }

// IsAlias reports whether a type decl spells an alias declaration
// (`type X = int`) rather than a defined type (`type X int`) — the `=`
// in the spec is the only difference, so the two forms partition type
// decls. An alias denotes its target: it earns no directives and no
// methods of its own. Enum-ness is orthogonal — a const may still be
// typed with the alias (EnumMembers lists it). Non-type symbols trap.
func IsAlias(s *Decl) bool { panic("minigo intrinsic") }

// TypeFields returns the member elements of a composite type
// expression — a struct spelling's fields (names, type, tag) or an
// interface spelling's elements — so tags inside anonymous struct
// types are readable where no decl names the composite. Other shapes
// trap.
func TypeFields(te *TypeExpr) []*Field { panic("minigo intrinsic") }

// Children drills into a composite type expression: []T -> T,
// map[K]V -> K then V, func(A) B -> A then B, and F[A] -> F then A —
// a generic instantiation's base leads its type arguments, so walks
// reach the generic decl itself (F in F[A]) not just its arguments.
func Children(te *TypeExpr) []*TypeExpr { panic("minigo intrinsic") }

// UnWrap peels one declared-type layer.
func UnWrap(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// UnRef strips one pointer layer.
func UnRef(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// Origin chases pointers and type transitions to the base expression.
func Origin(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// SymbolID resolves a type expression to the SymbolID it names — nil
// when it names nothing (a composite like []T, or an instantiation
// like Pair[int] whose base is a child, not the expression itself).
// The pointer return is the (SymbolID, bool) question a script can
// ask directly: sid == nil compiles and reads as real Go.
func SymbolID(te *TypeExpr) *runtime.SymbolID { panic("minigo intrinsic") }

// Resolve follows a type expression to its declaration view.
func Resolve(te *TypeExpr) *Decl { panic("minigo intrinsic") }

// UsedSymbols lists every imported member a file references.
func UsedSymbols(f *File) []runtime.SymbolID { panic("minigo intrinsic") }

// SameType reports strict structural equality over type expressions.
func SameType(a, b *TypeExpr) bool { panic("minigo intrinsic") }

// Initializer returns the initializer expression of a var or const
// decl as a TypeExpr view, or nil when the spec declares no value.
func Initializer(s *Decl) *TypeExpr { panic("minigo intrinsic") }

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
