package inspect

import "github.com/podhmo/minigo/runtime"

// Script-facing API. The engine binds "minigo.dev/inspect" and this
// package's module path to intrinsics — these bodies never execute
// inside minigo; they exist so scripts stay gopls/gofmt friendly.
// Views are returned boxed, so their fields (s.Name, f.Type.Text, ...)
// work in scripts without further declarations.
//
// Loading paths — which loader answers which object:
//
//	PackageOf   the canonical object: a bound registration wins;
//	            misses build lenient source into a private cache.
//	BoundOf     bound only: the registered host package, or nil when
//	            the path is not bound — never falls through to source.
//	SourceOf    source only: parses and indexes the source behind a
//	            bound path into its own cache, so the bound package
//	            keeps answering real imports; for an unbound path it
//	            answers the canonical package itself.
//	DirOf       source only, lenient: a directory never names a bound
//	            package — the parsed view always answers.
//	FileOf      a single file as a one-file package, lenient.
//
// Every loader has a Lookup* twin answering nil on a miss — a path
// that names nothing — while a found-but-broken target still errors:
// LookupPackageOf PackageOf's, LookupDirOf DirOf's, LookupSourceOf
// SourceOf's, LookupFileOf FileOf's.
//
// What the object offers differs by path: a bound package carries no
// index — Decls/Symbol synthesize Kind "host" pseudo-decls from its
// symbols, AST/Body/Pos trap, and Standard() is the registration's
// say-so (Bind marks dot-less first path elements standard). A
// source-built package indexes real declarations — Decls, Fields,
// AST, Body all work — and Standard() reports whether its resolved
// directory lives under GOROOT. Lenient builds (every dir/file
// locator, PackageOf's source miss, SourceOf's bound-path source)
// index source that would not compile, keeping duplicate decls; the
// canonical loaders (imports, Engine.Package, SourceOf on an unbound
// path) are strict.

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

// LookupSourceOf is SourceOf's non-trapping twin: it answers nil when
// the path resolves to no source — unclaimed by every module — while a
// located source that fails to build or a denied path still errors,
// the same missing-vs-broken split LookupPackageOf keeps. A bound path
// with no source behind its shadow (a synthetic bound path) reports
// nil too, so probing "is this path's source reachable?" never traps.
func LookupSourceOf(path string) *runtime.Package { panic("minigo intrinsic") }

// BoundOf returns the host-bound package registered for path — the
// object a script import answers when a bound shadow exists — or nil
// when the path is not bound. It never falls through to source: an
// unbound path is simply not bound, so "does the bound loader answer
// this path?" is a one-liner instead of a Standard()/Decls probe.
// Package modes gate imports, not this lookup — a path denied by
// WithPackageModes still reports its registration.
func BoundOf(path string) *runtime.Package { panic("minigo intrinsic") }

// DirOf loads a package by directory.
func DirOf(dir string) *runtime.Package { panic("minigo intrinsic") }

// LookupDirOf is DirOf's non-trapping twin: it answers nil when the
// path names nothing on disk — no such directory — while a path that
// exists but yields no package (a non-.go file, a dir with no
// buildable Go files) or one denied by policy still errors, so
// "missing" stays distinguishable from "found but unusable".
func LookupDirOf(dir string) *runtime.Package { panic("minigo intrinsic") }

// LookupPackageOf is PackageOf's non-trapping twin: it answers nil
// when the import path resolves to no directory — unclaimed by every
// module — while a located directory that fails to read (no buildable
// Go files) or a denied path still errors, the same missing-vs-broken
// split LookupDirOf keeps.
func LookupPackageOf(path string) *runtime.Package { panic("minigo intrinsic") }

// FileOf loads a single file as a one-file package, answering its
// file view.
func FileOf(file string) *File { panic("minigo intrinsic") }

// LookupFileOf is FileOf's non-trapping twin: it answers nil when the
// path names no file on disk — no such file — while a file that exists
// but fails to parse, or a path denied by policy, still errors, the
// same missing-vs-broken split LookupDirOf keeps.
func LookupFileOf(file string) *File { panic("minigo intrinsic") }

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

// Decls lists a package's (or one file's) top-level declarations —
// funcs, types, vars, and consts. Method declarations are NOT
// included: they attach to their receiver type, not the package
// namespace, so a scan walking Decls alone silently misses every
// method (including its doc comment). Methods(typeDecl) reaches them
// per type; Methods(pkg) or Methods(file) enumerates them flat.
func Decls(x any) []*Decl { panic("minigo intrinsic") }

// Symbol looks up one top-level declaration by name. Methods are not
// named here — reach them through Methods.
func Symbol(p *runtime.Package, name string) *Decl { panic("minigo intrinsic") }

// LookupSymbol is Symbol's non-trapping twin: it answers nil when the
// package has no such top-level name instead of trapping, so probing
// for an optional symbol is a one-liner. The argument contract is
// unchanged — a non-package value still errors.
func LookupSymbol(p *runtime.Package, name string) *Decl { panic("minigo intrinsic") }

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

// Comments enumerates a file's comments in source order — every //
// or /* */ comment, one Comment per written comment. Free marks the
// comments no declaration claims: inside function bodies, floating
// between decls, trailing the file — the ones Doc cannot reach.
// Text keeps the // or /* */ markers as written, so directive-shaped
// comments (//go:generate) stay visible.
func Comments(f *File) []*Comment { panic("minigo intrinsic") }

// Kind reports a symbol's kind: func|method|var|const|type|host.
func Kind(s *Decl) string { panic("minigo intrinsic") }

// Doc returns a symbol's doc comment text with the trailing newline
// removed — emitters can place it directly (Field.Doc and File.Doc
// follow the same contract).
func Doc(s *Decl) string { panic("minigo intrinsic") }

// Pos returns a decl's declaring position — nil for host symbols.
// The *Position view carries File/Line/Column fields so scripts
// never split "file:line:col" text.
func Pos(s *Decl) *Position { panic("minigo intrinsic") }

// Fields returns the declared fields of a struct type symbol, or the
// member elements of an interface type symbol (named method specs and
// embedded/constraint elements).
func Fields(s *Decl) []*Field { panic("minigo intrinsic") }

// Methods lists method declarations: on a type decl, the type's own
// methods; on a package or file view, every method declared there, in
// source position order — the receiver-attached decls Decls does not
// cover. MethodSet adds promoted members to the picture.
func Methods(x any) []*Decl { panic("minigo intrinsic") }

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
// const declarations typed with it — `const X Status = ...` specs,
// empty specs that inherit the type (`B` under `A Status = e`), and
// specs whose type is only inferred from an untyped initializer
// (`Microsecond = 1000 * Nanosecond`, whose Duration type lives on
// the materialized value). Exported members come first in declaration
// order, then unexported sentinels such as minDuration. A member only
// counts when its value materializes as a constant scalar — a
// composite "const" (`const P Point = Point{...}`, tolerated by the
// interpreter but not a Go constant expression) is not an enum value.
// Untyped constants and foreign-typed ones never list, so an empty
// result means the type is not an enum. Non-type symbols trap.
func EnumMembers(s *Decl) []*Decl { panic("minigo intrinsic") }

// MReqs returns the named member requirements of an interface type
// symbol — the method specs; embedded/constraint elements are skipped.
// Non-interface type decls return nil; non-type decls trap.
func MReqs(s *Decl) []*Field { panic("minigo intrinsic") }

// IEmbeds returns the embedded elements of an interface type symbol —
// embedded interfaces and constraint elements (~T, unions).
// Non-interface type decls return nil; non-type decls trap.
func IEmbeds(s *Decl) []*TypeExpr { panic("minigo intrinsic") }

// AST returns the raw AST node anchoring a decl: *ast.FuncDecl for
// funcs and methods, *ast.TypeSpec for type decls, *ast.ValueSpec for
// var and const decls — plainly, so fields like .Body and methods
// like .Pos() resolve through the host reflect facade. On a body
// Node it returns that node's own raw ast.Node. Host symbols trap.
func AST(x any) any { panic("minigo intrinsic") }

// Body returns a func or method decl's body as a Node — the root
// block (Kind "BlockStmt") whose children are the body's statements,
// so scripts can walk the statements and expressions a signature
// stops short of (the route registrations inside a registerRoutes
// body, the calls inside a task). A func decl without a body reports
// nil; host symbols and non-func decls trap.
func Body(s *Decl) *Node { panic("minigo intrinsic") }

// Nodes returns a node's children in source order — every statement,
// expression, and decl node the AST links from its fields, each
// tagged with the field name it fills as its Role: a CallExpr's
// children are Fun then Args, an IfStmt's are Init?, Cond, Body,
// Else?, and a BlockStmt's are its List. Optional parts keep their
// roles, so they stay distinguishable without positional guessing.
func Nodes(n *Node) []*Node { panic("minigo intrinsic") }

// Expr wraps an expression node as a decl-anchored TypeExpr — the
// names it mentions resolve against the declaring file, so
// SymbolID/Resolve/Children apply (`strings.TrimSpace` inside a body
// resolves to {strings, TrimSpace}). Non-expression nodes report nil.
func Expr(n *Node) *TypeExpr { panic("minigo intrinsic") }

// Signature returns a func/method's {Recv, Params, Results}. Params
// and Results keep one element per written field group — `a, b int`
// is one element carrying two Names — so an arity check ("how many
// params does the function take") goes through the Sig's
// NumParams()/NumResults(), not len(Params).
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
// trap: the same error host TypeFieldsOf reports, kept loud on
// purpose — a walk that only wants composites gates on Kind
// (StructType/InterfaceType) instead of silently losing fields.
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

// Value materializes a package member's runtime value. Var members and
// in-VM lookups may run the package initializer; constants bind without
// it (a constant needs no init).
//
// A const member arrives as its materialized scalar: an untyped const
// as its Go-native value (int64, float64, string, bool), a named-basic
// const as its underlying scalar (a `Duration` const as int64
// nanoseconds, a host-scalar typedef as the host scalar). A constant
// with no scalar representation — a composite literal — keeps its
// runtime value. Var members arrive as writable cells: read them
// through `*v`.
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
