package inspect

import "github.com/podhmo/minigo/runtime"

// Script-facing API: the engine binds "minigo.dev/inspect" and this
// package's module path to intrinsics (installInspect in the root
// package), so these bodies never execute inside minigo.
//
// Loader caches: PackageOf answers from the canonical cache (a bound
// registration wins) and builds misses leniently into a private cache.
// SourceOf builds the source behind a bound path into its own cache so
// the bound package keeps answering real imports; for an unbound path it
// is the canonical (strict) load. DirOf and FileOf build leniently. The
// canonical loaders (imports, Engine.Package) are strict.

// PackageOf returns the package with the given import path: the
// host-bound package when one is registered, else the package built from
// source. [SourceOf] reads the source behind a bound path instead, and
// [BoundOf] only reports the binding. Source is loaded even if it would
// not compile. It traps when the path does not resolve or fails to load;
// [LookupPackageOf] returns nil instead for a path that resolves to
// nothing.
func PackageOf(path string) *runtime.Package { panic("minigo intrinsic") }

// SourceOf returns the package built from the Go source of an import path
// even when a host-bound package shadows it, as it does for much of the
// standard library; imports keep getting the bound package. For an unbound
// path it returns the same package an import gets. Reading values ([Value])
// of such source may trap on constructs the interpreter does not support;
// declarations and syntax are the intended use. A package mode that
// denies the import of a bound path does not stop SourceOf from reading
// its source. It traps when path resolves to no source; [LookupSourceOf]
// returns nil instead.
func SourceOf(path string) *runtime.Package { panic("minigo intrinsic") }

// LookupSourceOf is [SourceOf] returning nil when the path resolves to no
// source, including a bound path with no source behind it. Source that
// fails to load, or a path outside the allowed roots
// ([github.com/podhmo/minigo.WithAllowedRoots]), still traps.
func LookupSourceOf(path string) *runtime.Package { panic("minigo intrinsic") }

// BoundOf returns the host-bound package registered for path, or nil when
// path is not bound. It never loads source. Package modes
// ([github.com/podhmo/minigo.WithPackageModes]) do not apply: a denied path
// still reports its registration.
func BoundOf(path string) *runtime.Package { panic("minigo intrinsic") }

// DirOf returns the package in directory dir. Unlike [PackageOf] it always
// builds from source: a directory never names a host-bound package. Source
// is loaded even if it would not compile, so duplicate declarations stay
// visible. It traps when dir does not exist; [LookupDirOf] returns nil
// instead.
func DirOf(dir string) *runtime.Package { panic("minigo intrinsic") }

// LookupDirOf is [DirOf] returning nil when dir does not exist. A path
// that exists but holds no buildable Go package, or is denied by policy,
// still traps.
func LookupDirOf(dir string) *runtime.Package { panic("minigo intrinsic") }

// LookupPackageOf is [PackageOf] returning nil when the import path
// resolves to no directory. A located package that fails to load (no
// buildable Go files), or a denied path, still traps.
func LookupPackageOf(path string) *runtime.Package { panic("minigo intrinsic") }

// FileOf loads a single Go file as a one-file package and returns its
// [File]. Source is loaded even if it would not compile. It traps when
// file does not exist; [LookupFileOf] returns nil instead.
func FileOf(file string) *File { panic("minigo intrinsic") }

// LookupFileOf is [FileOf] returning nil when file does not exist. A file
// that fails to parse, or a path denied by policy, still traps.
func LookupFileOf(file string) *File { panic("minigo intrinsic") }

// Packages lists every Go package directory under root, across nested
// modules, sorted by directory. Each directory holding .go files is
// attributed to its nearest enclosing go.mod. Directories the go tool
// ignores (vendor, testdata, names starting with "." or "_") are skipped.
// Nothing is parsed: load a result with [DirOf] on its Dir, or read one of
// its files with [FileOf] (for example a test file's imports). It traps if
// root does not exist or a go.mod does not parse; a tree without Go files
// yields an empty slice.
func Packages(root string) []*RepoPackage { panic("minigo intrinsic") }

// Current returns the package of the calling function.
func Current() *runtime.Package { panic("minigo intrinsic") }

// OwnerOf returns the package that declares v: a function, method value,
// type, value of a declared struct type, package, or host value of a named
// Go type. It returns nil for a host value with no known package and traps
// for other values.
func OwnerOf(v any) *runtime.Package { panic("minigo intrinsic") }

// PathOf returns the import path of the package [OwnerOf] reports for v.
func PathOf(v any) string { panic("minigo intrinsic") }

// SymbolIDOf returns the identity of the declaration [SymbolOf] finds for
// v.
func SymbolIDOf(v any) runtime.SymbolID { panic("minigo intrinsic") }

// SymbolOf returns the declaration behind v: a function, method value,
// type, value of a declared struct type, or host function. It returns nil
// when v has no top-level declaration (a function literal, for example) and
// traps for a package or other values.
func SymbolOf(v any) *Decl { panic("minigo intrinsic") }

// Decls lists the top-level funcs, types, vars and consts of a package or
// of one [File]. Methods are not included; list them with [Methods]. For a
// host-bound package it lists Kind "host" decls of the bound members.
func Decls(x any) []*Decl { panic("minigo intrinsic") }

// Symbol returns the top-level declaration called name in p, or a Kind
// "host" decl for a member of a host-bound package. Methods are not found here;
// use [Methods]. It traps when p has no such name; [LookupSymbol] returns
// nil instead.
func Symbol(p *runtime.Package, name string) *Decl { panic("minigo intrinsic") }

// LookupSymbol is [Symbol] returning nil when p has no such name. An
// argument that is not a package still traps.
func LookupSymbol(p *runtime.Package, name string) *Decl { panic("minigo intrinsic") }

// Implementers returns the type declarations of p whose method set (see
// [MethodSet]) covers every method iface requires, including those of
// interfaces it embeds. Pointer-receiver methods count. Interface types
// that embed the requirements count too, and iface itself is included;
// filter on [Def](d).Kind for concrete types. Only p is searched.
//
// iface must be an interface type declaration. It traps for a constraint
// interface (one with ~T terms, unions or embedded non-interface types),
// which no value type implements, and for any other declaration.
//
// A host-bound interface, embedded or passed as iface, requires what its
// binding declares: fmt.Stringer requires String() string. One whose
// binding names its methods without signatures, such as io.Reader, is an
// unknown requirement and traps rather than matching every type.
func Implementers(p *runtime.Package, iface *Decl) []*Decl { panic("minigo intrinsic") }

// Files lists the source files of p; a host-bound package has none.
func Files(p *runtime.Package) []*File { panic("minigo intrinsic") }

// Imports lists f's own imports.
func Imports(f *File) []*Import { panic("minigo intrinsic") }

// Comments lists every // and /* */ comment of f in source order, one
// [Comment] each, markers included. Unlike [Doc] it also reaches comments
// inside function bodies, between declarations and at the end of the file;
// Comment.Free marks those.
func Comments(f *File) []*Comment { panic("minigo intrinsic") }

// Kind returns s.Kind: "func", "method", "var", "const", "type" or
// "host".
func Kind(s *Decl) string { panic("minigo intrinsic") }

// Doc returns s's doc comment text without the trailing newline, or "".
// The Doc fields of [Field] and [File] follow the same rule.
func Doc(s *Decl) string { panic("minigo intrinsic") }

// Pos returns where s is declared, or nil when that is unknown, as for
// most host symbols.
func Pos(s *Decl) *Position { panic("minigo intrinsic") }

// Fields returns the fields of a struct type declaration, or the elements
// of an interface type declaration: method specs are named and have a
// func-type Type, while embedded interfaces and constraint elements (~T,
// unions) are Embedded. It traps for other declarations; read other type
// shapes through [Def].
func Fields(s *Decl) []*Field { panic("minigo intrinsic") }

// Methods lists method declarations; unlike [MethodSet] it omits promoted
// methods. For a type declaration it returns the type's own methods,
// sorted by name. For a package or [File] it returns every method declared
// there in source order, including methods whose receiver type is not
// declared. It traps for host-bound packages and types.
func Methods(x any) []*Decl { panic("minigo intrinsic") }

// MethodSet returns the method set of a type declaration, sorted by name;
// unlike [Methods] it includes methods promoted through embedded fields.
// It holds the members usable through a *T: the type's own methods with
// either receiver (unlike Go's method set of T), plus promoted members,
// transitively.
//
// Promotion follows Go: an embedded T contributes its value-receiver
// methods, an embedded *T or interface contributes all, and an embedded
// pointer anywhere on the path keeps deeper pointer-receiver methods. Alias
// and generic-instantiation embeds count as their underlying type; type
// parameters contribute nothing. A shallower member hides a deeper one
// with the same name, and distinct members at the same depth cancel out as
// ambiguous; one declaration reached along two paths counts once.
//
// It traps for non-type declarations.
func MethodSet(s *Decl) []*Method { panic("minigo intrinsic") }

// EnumMembers returns the constants in the type's package that have the
// type: those declared with it (`const X Status = ...`), those inheriting
// it in a const group (`B` under `A Status = iota`), and those whose
// untyped initializer has the type (`Microsecond = 1000 * Nanosecond`).
// Exported members come first, then unexported ones, each in declaration
// order. A constant counts only if its value is a scalar: a composite
// "const" the interpreter tolerates (`const P Point = Point{...}`) is not
// a member. An empty result means the type is not an enum. It traps for
// non-type declarations.
func EnumMembers(s *Decl) []*Decl { panic("minigo intrinsic") }

// MReqs returns the method specs of an interface type declaration, each
// named and with a func-type Type; embedded elements are left to
// [IEmbeds]. It returns nil for a non-interface type and traps for other
// declarations.
func MReqs(s *Decl) []*Field { panic("minigo intrinsic") }

// IEmbeds returns the embedded elements of an interface type declaration:
// embedded interfaces and constraint elements (~T, unions). It returns nil
// for a non-interface type and traps for other declarations.
func IEmbeds(s *Decl) []*TypeExpr { panic("minigo intrinsic") }

// AST returns the go/ast node behind x. For a [Decl] that is the
// *ast.FuncDecl of a func or method, or the *ast.TypeSpec or
// *ast.ValueSpec of a type, var or const; for a [Node] it is the node
// itself. Scripts reach the node's fields and methods (.Body, .Pos()) as on
// any host value, and go/ast's own walkers (ast.Inspect, ast.Walk) accept
// it. It traps for host decls.
//
// A go/ast node does not lead back to a [Node]. A walker that also
// resolves names walks [Body]/[Nodes], dispatches on AST(n), and hands the
// child Node filling the wanted field (Role "Fun" for call.Fun) to [Expr].
func AST(x any) any { panic("minigo intrinsic") }

// Body returns the body of a func or method declaration as a [Node] of
// Kind "BlockStmt" whose children ([Nodes]) are its statements. It
// returns nil for a function declared without a body and traps for host
// and non-func declarations.
func Body(s *Decl) *Node { panic("minigo intrinsic") }

// Nodes returns n's child nodes in source order, each with Role set to the
// go/ast field it fills: a CallExpr's children are Fun then Args, an
// IfStmt's are those of Init, Cond, Body and Else that are present, and a
// BlockStmt's are its List.
func Nodes(n *Node) []*Node { panic("minigo intrinsic") }

// Expr returns an expression [Node] as a [TypeExpr], so the names it
// mentions resolve against the declaring file with [SymbolID] and
// [Resolve]: `strings.TrimSpace` in a body resolves to {strings,
// TrimSpace}. A name bound locally in the enclosing function yields no
// SymbolID rather than a package's, as does an identifier that names no
// declaration: a selector's member name, a label, a declared name, the
// blank, and a struct-literal key (a map or array literal's key still
// resolves). Expr returns nil for statements and declarations.
func Expr(n *Node) *TypeExpr { panic("minigo intrinsic") }

// Signature returns the signature of a func or method declaration, or of a
// host function whose Go type is known. Params and Results hold one
// [Field] per written group (`a, b int` is one), so check arity with
// [Sig.NumParams] and [Sig.NumResults], not len(Params). It traps for
// other declarations.
func Signature(s *Decl) *Sig { panic("minigo intrinsic") }

// TypeParams returns the type parameters of a generic func or type
// declaration, and nothing for other declarations. It traps for host
// decls.
func TypeParams(s *Decl) []*Field { panic("minigo intrinsic") }

// Def returns the right-hand side of a type declaration: `struct{...}` for
// `type T struct{...}`, `int` for `type ID int` or `type ID = int`. For a
// host decl with a known Go type it returns that type. It traps for other
// declarations.
func Def(s *Decl) *TypeExpr { panic("minigo intrinsic") }

// DeclType returns the type written on a var or const declaration (`var x
// Status`, `const X Status = ...`), or the type a const inherits in a group
// (`B` under `A Status = iota`). It returns nil for an untyped declaration
// and traps for other kinds.
func DeclType(s *Decl) *TypeExpr { panic("minigo intrinsic") }

// IsAlias reports whether a type declaration is an alias (`type X = int`)
// rather than a defined type (`type X int`). An alias declares no methods
// of its own; constants typed with it still appear in [EnumMembers]. It
// traps for non-type declarations.
func IsAlias(s *Decl) bool { panic("minigo intrinsic") }

// TypeFields is [Fields] for a struct or interface type expression rather
// than a declaration, so anonymous struct types and their tags can be
// read. It traps for other expressions; check Kind ("StructType",
// "InterfaceType") first.
func TypeFields(te *TypeExpr) []*Field { panic("minigo intrinsic") }

// Children returns the component expressions of te: T for []T, [N]T, *T,
// chan T and ...T; K then V for map[K]V; F then the arguments for F[A, B];
// parameter then result types for a func type; field types for struct and
// interface types; T for ~T and A then B for A | B. Other expressions have
// none.
func Children(te *TypeExpr) []*TypeExpr { panic("minigo intrinsic") }

// UnWrap replaces a name of a declared type with its definition ([Def]):
// for `type ID int`, ID becomes int. An alias is one layer too. Other
// expressions, and names that do not resolve to a type, come back
// unchanged.
func UnWrap(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// UnRef returns T for *T, and te unchanged otherwise.
func UnRef(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// Origin applies [UnRef] and [UnWrap] until neither changes the
// expression. A recursive declaration such as `type Node *Node` stops at
// the repeated name.
func Origin(te *TypeExpr) *TypeExpr { panic("minigo intrinsic") }

// SymbolID returns the identity of the declaration a type expression
// names, resolving a qualified name through the declaring file's imports.
// Predeclared names — types, funcs such as len, constants and nil —
// report [BuiltinPackagePath] unless the package declares the same name,
// and type parameters report [TypeParamPackagePath]. It returns nil for
// anything but a plain or qualified name, including composites like []T
// and instantiations like Pair[int], whose base [Children] reaches, and
// for a bare import name such as the strings of strings.TrimSpace.
func SymbolID(te *TypeExpr) *runtime.SymbolID { panic("minigo intrinsic") }

// Resolve returns the [Decl] that te names, loading its package if needed.
// A member of a host-bound package resolves to the Kind "host" decl
// [Symbol] reports for it. It returns nil when [SymbolID] is nil or names
// a builtin or type parameter, and traps when the declaration cannot be
// found; [LookupResolve] returns nil instead.
func Resolve(te *TypeExpr) *Decl { panic("minigo intrinsic") }

// LookupResolve is [Resolve] returning nil when the declaration cannot be
// found: the import path resolves to no directory, or the package has no
// such name. A located package that fails to load still traps. Since a
// trap cannot be recovered, a walk that must survive unresolvable names
// calls this instead of Resolve.
func LookupResolve(te *TypeExpr) *Decl { panic("minigo intrinsic") }

// UsedSymbols lists the imported package members f refers to as pkg.Name,
// each once, in order of first use. A select through a local that shadows
// the import, such as a param named strings, is not a use.
func UsedSymbols(f *File) []runtime.SymbolID { panic("minigo intrinsic") }

// SameType reports whether a and b spell the same type. Names compare by
// the declaration they resolve to, so an alias and its target differ;
// composites compare kind, components, array length and channel
// direction. A function-local name, which has no [SymbolID], matches
// only the same name under the same [Body].
func SameType(a, b *TypeExpr) bool { panic("minigo intrinsic") }

// Initializer returns the value expression of a var or const declaration:
// e2 for b in `var a, b = e1, e2`, and f() for both in `var a, b = f()`.
// A const that omits its value in a group gets the expression it
// inherits. It returns nil when there is no initializer and traps for
// other kinds.
func Initializer(s *Decl) *TypeExpr { panic("minigo intrinsic") }

// Value returns the runtime value of the package member name. Reading a
// var may run the package's initialization; reading a const does not.
//
// A const arrives as a plain scalar: an untyped const as int64, float64,
// string or bool, and a const of a named basic type as its underlying
// value (a const of `type Duration int64` as an int64). A const with no
// scalar form keeps its runtime value. A var arrives as a cell; read it
// with `*v`.
func Value(p *runtime.Package, name string) any { panic("minigo intrinsic") }

// TypeOf returns the [runtime.TypeDef] of a type declaration. It traps for
// other declarations.
func TypeOf(s *Decl) any { panic("minigo intrinsic") }

// Name returns p's package name.
func Name(p *runtime.Package) string { panic("minigo intrinsic") }

// Path returns p's import path.
func Path(p *runtime.Package) string { panic("minigo intrinsic") }

// Dir returns p's directory, with forward slashes.
func Dir(p *runtime.Package) string { panic("minigo intrinsic") }

// State returns how far p has been loaded: "unseen", "located", "parsed",
// "indexed", "initializing", "ready" or "failed".
func State(p *runtime.Package) string { panic("minigo intrinsic") }

// Standard reports whether p is a standard library package: for a source
// package, whether its directory is under GOROOT; for a host-bound
// package, whether its import path's first element has no dot.
func Standard(p *runtime.Package) bool { panic("minigo intrinsic") }
