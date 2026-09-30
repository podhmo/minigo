# Plan: package objects / symbol introspection

Status: design draft (supersedes the "Future works" note in plan-minigo-vm.md)

Goal: a script-facing view over the package world — packages, their
files, imports, and declarations — that works **as syntax, not as
evaluated values**. Two clients share one capability surface:

- **Scripts** import a bound stub package (`minigo.dev/inspect`,
  gopls-friendly `panic("minigo intrinsic")` bodies, like `host`).
- **The REPL** accesses the same machinery implicitly: `:cd <ref>`
  changes the ambient package (pseudo dot-import), `:ls` enumerates
  the current package's members.

## Motivation: the SSOT schema file

The driving use case is a *schema* file — one file with many imports
(e.g. `db` types) containing the struct definitions of record. The
existing tools each lose something:

- `go/packages`/`go/types` loads the whole transitive import graph
  eagerly just to answer "what fields does this struct have" — and is
  banned here anyway.
- Raw `go/parser` reaches the same decls but at AST granularity:
  hand-walking `*ast.GenDecl`/`TypeSpec`/`Field`/`StarExpr` is
  tedious and error-prone.
- Runtime `reflect` gives clean field iteration but has already lost
  filenames, positions, doc comments, and the declaring package.

The inspect surface sits in the gap: decl-granular views (one level
above raw AST), positions/docs preserved via the FileSet, and
**laziness as the headline property** — `SymbolID` on a `db.User`
field type resolves to `{db, User}` through the file's import table
*without loading db at all*; `Resolve` costs exactly one package
(parse+index, still no init, still no compile). Following a field
type across packages pays for exactly the packages actually touched.

## Why syntax-level, not value-level

The headline use is browsing structure: a `Symbol` naming a struct
type should hand back its *declared* fields — names, type
expressions, tags, doc comments — without ever running package
initializers. Everything needed already exists at index time:

- `index.Decl` carries `Kind`, `Name`, `Spec` (ast), `Gen`, `Func`,
  `File`, `Pos`.
- `syntax.File` carries `Imports` (`Path`, `Alias`, `LocalName()`) and
  the parsed `*ast.File` (comments enabled, so `Doc()` works).
- `runtime.SymbolID{PackagePath, Name}` is the canonical identity,
  reused from special forms / vet.

So the API's core layer never materializes and never initializes —
it reports declarations as declared.

## Layers

| Layer | Entry points | Cost | Side effects |
|---|---|---|---|
| index | `PackageOf`, `DirOf`, `FileOf`, `Current`, `Decls`, `Files`, `Imports`, `OwnerOf`, `PathOf`, `SymbolIDOf`, `Standard` | locate + parse + index | none |
| syntax | `Doc`, `Pos`, `Fields`, `Methods`, `Signature`, `TypeExpr` views, `Children`, `UnWrap`, `UnRef`, `Origin`, `SymbolID`, `Resolve`, `SameType`, `UsedSymbols` | AST reads + import-table lookup | none |
| value | `Value`, `TypeOf`, `Kind` | materialize | `Value` on var/const runs `EnsureReady` (package init) |

The value layer is deliberately a small, explicit annex — reaching a
`var`/`const` member must be an opt-in (`Value`) because it may run
init side effects. `TypeOf`/`Kind` materialize decls only
(functions and typedefs are init-free even under the default
`GoCompatibleInit`). `Methods` returns *decl* views from
`TypeDeclInfo.Methods` — index-level, not the materialized method set.

## Script API sketch

```go
import "minigo.dev/inspect"   // stub package; engine binds intrinsics

// package access — locators on the left, symbol handles on the right
p := inspect.PackageOf("strings")           // fake import: loadPath -> Indexed
q := inspect.DirOf("./app")                 // dir entry point
f := inspect.FileOf("./schema.go")          // single-file package (Engine.LoadFile)
p2 := inspect.OwnerOf(strings.Contains)     // symbol -> the "strings" package
self := inspect.Current()                   // caller's *runtime.Package

inspect.PathOf(strings.Contains)           // -> "strings"
inspect.SymbolIDOf(source.SrcUser)         // -> {model.Path, "SrcUser"}
s := inspect.SymbolOf(dst.DstUser)         // value -> decl view (see below)

inspect.Name(p) / Path(p) / Dir(p) / State(p)
inspect.Standard(p)                 // bool — inside GOROOT (PackageMeta.Standard)
inspect.Decls(p)                    // []Symbol — top-level decls, index-level,
                                    // no init; Decls(f) filters to one file
inspect.Symbol(p, "Contains")       // one decl
inspect.Files(p)                    // []File{Name, Imports[{Path, Name, Pos}], Doc}
inspect.Imports(f)                  // the file's own import table
inspect.SymbolID(s)                 // {PackagePath, Name}

// syntax layer — s is a Symbol (decl view)
inspect.Kind(s)                     // "func"|"method"|"var"|"const"|"type"|"host"
inspect.Doc(s); inspect.Pos(s)      // doc comment text; "file.go:12:6"
inspect.Fields(s)                   // struct type -> []Field
inspect.Methods(s)                  // type decl -> []Symbol (method decls)
inspect.Signature(s)                // func/method -> {Recv, Params, Results}
inspect.TypeParams(s)               // generic decl's type parameter fields
inspect.Def(s)                      // type decl -> its declared TypeExpr
```

`Field` is a view over `*ast.Field` + the declaring file:

```
Field{ Names []string, Type *TypeExpr, Tag string, Doc string, Embedded bool, Pos }
TypeExpr{ Text string /* format.Node */, Kind string /* "Ident", "SelectorExpr",
          "StarExpr", "ArrayType", "MapType", "ChanType", "FuncType", ... */ }
inspect.Children(te)  // []TypeExpr — drill into composite exprs:
                      // []*db.User -> *db.User -> db.User
inspect.UnWrap(t)     // peel one declared-type layer: a newtype's
                      // underlying TypeExpr; other shapes pass through
inspect.UnRef(t)      // strip one pointer layer: *T -> T
inspect.Origin(t)     // chase the whole declared chain: pointers AND
                      // type transitions — a newtype counts as one step
                      // (type A B -> B), an alias (type A = B) is
                      // transitive. Terminates on the base decl:
                      // Origin(x) == x at the end of the chain
inspect.SymbolID(te)  // Ident/SelectorExpr -> SymbolID via the *declaring*
                      // file's import table (not the caller's)
inspect.Resolve(te)   // SymbolID -> Symbol (decl) — follow types across packages
inspect.UsedSymbols(f) // []SymbolID — every imported member the file
                       // references (SelectorExpr on an import-local name);
                       // "what does this schema file depend on" in one call
inspect.SameType(a, b) // structural equality over TypeExpr trees:
                       // Ident/SelectorExpr compare by SymbolID,
                       // composite exprs compare Kind + Children —
                       // "does this src/dst field need a Map entry"
inspect.Value(p, name) // runtime value; may run init (var/const)
inspect.TypeOf(s)      // *TypeDef for a type symbol (materialize, no init)
```

`Fields → TypeExpr → Children/SymbolID → Resolve → Symbol → Fields`
lets a script walk a decl graph across packages in purely syntactic
terms — e.g. a `[]*db.User` field: `Origin` jumps straight to the
`db.User` decl (pointer + selector resolved in one step).

## Symbol → package, value → decl

The dual direction matters as much: in a script, `import "strings"`
is real Go — gopls completes `strings.Contains` — so the handle you
*type* is a member reference, and `inspect` turns it back into the
package/syntax world:

```go
import "strings"
import "minigo.dev/inspect"

p  := inspect.OwnerOf(strings.Contains)    // *runtime.Package
id := inspect.SymbolIDOf(x.Method)          // works on METHODS too
s  := inspect.SymbolOf(x.Method)            // -> method decl view
inspect.Fields(inspect.SymbolOf(dst.DstUser)) // IDE-completed value -> Fields
// s.Package is also a field on the Symbol view — SymbolOf(x).Package
```

Mapping by value kind:

| Value | `OwnerOf` | `SymbolOf` |
|---|---|---|
| `*ImportRef` / `*Package` | Materialize / self | — |
| `*Function`, `*Closure` | `Fn.Pkg` | func decl (index lookup) |
| `*BoundMethod` | `Fn.Pkg` | method decl via `TypeDeclInfo.Methods` |
| `*TypeDef` | `Pkg` | type decl via `Types` — see Go-validity note |
| `*Struct` (instance) | `Def.Pkg` | its `Def`'s type decl |
| `*BuiltinFunc` | new `Pkg` field stamped at `Bind` | Kind "host" pseudo-symbol — `Signature`/`Pos` recovered via `Target` (below) |
| `*GoValue` | `reflect.TypeOf(V).PkgPath()` → bound package | — |

Locator vs symbol access use different names deliberately
(`PackageOf` takes a path string; `OwnerOf` takes a value) — the
symbol->package direction keeps its own name rather than overloading
`PackageOf` on argument type. Every `Symbol` view also carries a
`Package` field back to its owning package, so `SymbolOf(x).Package`
is the idiomatic chain.

Note the Go wart this escapes: real Go can recover a function's
package via `runtime.FuncForPC(reflect.ValueOf(f).Pointer())`, but a
method *value*'s PC is a wrapper thunk — methods are unreachable that
way. In minigo `BoundMethod` keeps the declaring `*Function`, so
`SymbolOf`/`OwnerOf` cover methods for free. The same luck applies to
struct *instances*: `runtime.Struct.Def` points back at the declaring
`TypeDef`, so `OwnerOf(u)` on `u := model.SrcUser{...}` recovers
"myapp/model" — reflect on a Go value can't do that (the type name
survives only as a string, the file/decl is gone).

Go-validity note: `inspect.OwnerOf(strings.Contains)` and
`inspect.OwnerOf(u)` are valid Go expressions, but
`inspect.OwnerOf(model.SrcUser)` — passing a bare type name — is not
valid Go (a type is not an expression). It still parses and works in
minigo (evaluates to the `*TypeDef`), and it is kept as a minigo
extension because it is convenient — but scripts that must compile as
real Go should pass instances (`SymbolOf(model.SrcUser{})`) instead.

Cost caveat: evaluating `pkg.F` runs normal member semantics — under
the default `GoCompatibleInit`, a source package's init fires on first
member access. `SymbolOf` is the convenience bridge when you already
hold the value; for init-free decl access use `inspect.Symbol(pkg,name)`.

## Consumer story: scaffolding generation (convert-define)

The concrete dream: a minigo script that emits the *definitions*
file `examples/convert-define` expects — pairing decls across two
packages by name and printing Go fragments. The inspect layer covers
each step:

```go
src := inspect.PackageOf("myapp/model")     // e.g. db models
dst := inspect.PackageOf("myapp/api")       // e.g. DTO structs

for _, s := range inspect.Decls(src) {
    if s.Kind != "type" { continue }
    d := pairFor(s, inspect.Decls(dst))    // script-side rule:
                                           // name match, suffix strip, ...
    if d == nil { continue }
    emitConvertHeader(s, d)                // SymbolID -> qualified refs:
    // "func(c *define.Config, dst *api.DstUser, src *model.SrcUser)"
    for _, f := range inspect.Fields(s) {
        g := findField(inspect.Fields(d), f.Names[0])
        if g == nil {
            emit("// no counterpart: src.%s %s", f.Names[0], f.Type.Text)
        } else if !inspect.SameType(f.Type, g.Type) {
            // SymbolID.PackagePath of both sides -> needs a converter
            emit("\tc.Convert(dst.%s, src.%s, /*conv*/)", g.Names[0], f.Names[0])
        } // same name + same type -> auto-mapped, emit nothing
    }
}
```

What codegen needs and gets: field names/tags (`Field`), declared
type spellings (`TypeExpr.Text`), canonical references for building
the output file's import block (`SymbolID.PackagePath` + a chosen
alias), and same-type detection (`SameType` on SymbolIDs, not text —
two files spell `db.User` differently). The script writes the result
with the existing `os.WriteFile` intrinsic, so the whole pipeline is
`minigo run ./tools/genconv`.

The REPL workflow is the same pipeline interactively: `:cd myapp/model`,
`:ls`, drill `inspect.Fields`, prototype the pairing rule, then bake
it into the script.

## Return-shape decisions

- View structs (`Symbol`, `File`, `Field`, `TypeExpr`) are
  boxed as `*runtime.GoValue`: exported field access already works via
  the existing reflective member dispatch, and internals (raw
  `ast.Node`s) stay out of the FFI — `TypeExpr` keeps `ast.Expr` +
  declaring file as hidden context for `SymbolID`/`Resolve`/`UnWrap`/
  `UnRef`/`Origin`.
- `PackageOf`/`DirOf`/`FileOf`, `Current`, `OwnerOf`, `Value`/`TypeOf`
  return real runtime values (`*runtime.Package`, `*TypeDef`,
  `*Function`, `*runtime.SymbolID`-shaped struct) — they compose with
  everything a script can already do (`p.Sym`, `f(args)`, `T{...}`).
- Accept `*runtime.ImportRef` anywhere a package is expected, so
  `p := fmt; inspect.Decls(p)` works on a source import.

## Bound packages

`Bind`-registered packages have no index and *shadow* source loading
(`PackageOf("strings")` returns the bound package, not GOROOT
source — same as execution). `Decls` falls back to `Globals.Names()`
yielding `Kind:"host"` symbols, so bound stdlib intrinsics
introspect as the same object shape as source packages.
`inspect.Standard(p)` reports the `PackageMeta.Standard` flag.
Reaching a bound-shadowed package's real source is out of scope
(a `SourceOf` bypass could be added if it turns out to matter).

A bound `Fn` is an anonymous `func([]any)` adapter, so the real
signature is unrecoverable from the bound value itself — and a PC
taken on the adapter resolves to the closure's own symbol, not the
target's. `BuiltinFunc` therefore grows an optional `Target any`
field holding the underlying Go func value (one pointer slot; the
callee is already linked in since the adapter calls it). For a host
symbol, `Signature` synthesizes `Field` views from
`reflect.TypeOf(Target)` (param/result names are absent — positions
only) and `Pos` uses `runtime.FuncForPC(...).FileLine`.
`Doc` stays empty. `Target` is nil for intrinsics that don't declare
one; then `Signature`/`Pos` return nothing, same as a var/const.

## REPL: implicit access

- `:cd <ref>` — `REPL.Enter(ctx, ref)`: load the target and inject one
  pseudo `ImportRef{Alias:"."}` into the scratch package's import
  table on each `reload()`. Bare names resolve through the existing
  dot-import path. The pseudo ref carries a flag that bypasses the
  `token.IsExported` gate — `cd` means "enter the package", so
  unexported members are visible (the key difference from `import .`).
- `:cd` alone reports the current package; `:cd -` returns to `<repl>`.
- **cd changes name resolution only.** `x := ...` still hoists into the
  `<repl>` scratch package (option (a)); writing declarations *into*
  the visited package is a later, opt-in command (risky: `engine.pkgs`
  is shared, so mutating a Ready package affects every importer).
- `:ls [name]` — `Decls` of the current package, or `Fields`/`Methods`/
  `Signature` of a named symbol. CLI-side sugar over the same inspect
  intrinsics.

## Implementation notes

- `inspect/inspect.go` — stub declarations (host-package pattern);
  bound under both `minigo.dev/inspect` and the repo path.
- `inspect.go` (root) — the `Bind` table + view mapping. Needs engine
  internals (`loadPath`/`loadDir`, `materialize`, `fset` for
  positions, declaring-file import tables for `SymbolID`), so it
  installs in-process like `installStdlib`, not via `gen-intrinsics`.
- `VMCaller` grows `Package() *runtime.Package` (caller's package) to
  implement `Current` as an ordinary builtin.
- `runtime.BuiltinFunc` gains `Pkg *Package`, stamped by `Engine.Bind`
  after the package is constructed — bound intrinsics become
  `OwnerOf`-able (`inspect.PathOf(strings.Contains)` -> "strings").
  Predeclared builtins (`len`) keep nil → `OwnerOf` errors with a
  clear message.
- `runtime.ImportRef` gains an internal flag (e.g. `AllNames`) used by
  the REPL pseudo-import to skip the exported-name gate in
  `resolveGlobal`'s dot-import walk.
- `UsedSymbols` shares the same walk: per file, collect
  `SelectorExpr`s whose `X` resolves to an import-local name and map
  each through the import table to `SymbolID` — index-time work, no
  evaluation. Dot imports are invisible to this walk (same limitation
  as `vet`): an unqualified member reference can't be told from a
  local symbol.
- Predeclared idents (`int`, `string`, `error`, ...) map to
  `SymbolID{PackagePath: ":builtin:", Name}` — the pseudo path keeps
  `SameType`/`SymbolIDOf` total over builtin names.

## Open questions

- Package name: `minigo.dev/inspect` vs folding into `host` vs
  `minigo.dev/pkg`. `inspect` reads best for a browse-only surface.
- `SameType` semantics: SymbolID-equality is strict (declared-type
  identity). A looser mode (underlying-shape equality, alias
  transparency) may be needed for real codegen — start strict.
- Interface decls: expose `MReqs`/`IEmbeds` as `Methods`-like views?
- Whether `:cd` ever gains a write mode (`:pin`/`:edit`) for
  session-scoped patching of a package's globals.

## Round-2 notes: interface members, `SourceOf`, and `:cd` write-mode

The three deferred items landed in one pass; what building each one
revealed, in the order it was worked. Tests came first per item
(`testdata/inspectuse` script calls for the inspect surface,
`TestREPLPinWrite` for write mode).

### Interface members

- **One uniform member view, plus two filtered ones.** `Fields` reads
  interface decls too — `InterfaceType.Methods` is an `*ast.FieldList`,
  so the struct `fieldList` path already produces the right shape:
  named method specs keep `Names` + a FuncType `TypeExpr`, unnamed
  elements come back `Embedded` with their type expression. The plan's
  open question resolved as *both*: `MReqs`/`IEmbeds` split the two
  element kinds (names mirror `TypeDef.MReqs`/`IEmbeds`), `Fields`
  stays the flat view.
- **A union constraint is one element, not many.** `~int | ~int64` is a
  single field whose Type is a BinaryExpr — its `Children` are the
  tilde terms. `IEmbeds` returns the BinaryExpr as one TypeExpr;
  callers walk `Children`.
- `SymbolID`/`Resolve` on an embedded element chase the name back to
  its decl unchanged — no extra work once elements flow through the
  Field path.

### `SourceOf` — the deferred `Bind` bypass

- The design text left a bound-shadowed package's real source out of
  scope; it turned out to matter quickly — `PackageOf("strings")`
  answers a bound object whose `Index` is nil, so nothing below the
  name list was inspectable.
- **`SourceOf` returns a detached package, not the canonical one.** It
  caches into a private `e.srcs` and is deliberately never published to
  `pkgs`/`byDir` — the bound shadow is intentional, so importers and
  `PackageOf` keep seeing the bound object while the source copy
  serves inspection only.
- Unbound paths return the canonical package (`SourceOf == PackageOf`
  there) so the locator stays total and callers need no bound-check
  branch.
- Caveat: value-layer access (`Value`/`TypeOf`) on real GOROOT source
  can trap on unimplemented constructs; the index/syntax layers are
  the intended use. Noted on the stub doc.

### `:cd` write-mode — `:pin`/`:unpin`

- **Why cell sharing, not store redirection.** `OpSetGlobal` writes
  `f.fn.Pkg.Globals` — the *executing function's own* package — so a
  repl-frame `x = v` can never reach `entered.Globals` directly.
  Patching therefore works by aliasing: `Pin` (after `EnsureReady` so
  decl globals exist) binds the entered package's var *cells* into the
  repl scope, and writes through a shared cell land in the package.
- **`x := v` reuses or shadows into a published cell.** Hoist first
  asks `entered.Globals` for an existing writable cell and re-aliases
  it; a non-cell member (bound `BuiltinFunc`, materialized func) or a
  new name gets a fresh cell published at commit — which is also how
  bound package members get patched. Plain `x = v` on a non-cell
  member stays a repl-local shadow (`Set` writes raw); documented
  divergence: patching those needs `:=`.
- **Consts stay read-only.** A const's cell carries `ReadOnly`, and the
  shared cell keeps it — `Label = "x"` traps like any const assign.
- **Func/type decls bind one object in both scopes.** `commitWrites`
  materializes the decl through the repl package and sets the same
  value into `p.Globals`, so `F` and `pkg.F` keep one identity.
  `pinnedDecls` records them because reload's materialization eviction
  would otherwise split bare `T` (fresh typedef) from `pkg.T` (the
  published one) — found while wiring `x.(T)` identity.
- **Methods graft onto the index, then evict the typedef.**
  `typeDefOf` freezes `td.Methods` at materialization, so a patched
  method decl is recorded into `Index.Types[recv].Methods` *and* the
  entered package's cached `Globals[recv]` typedef is dropped — next
  access rebuilds the method set. `index.ReceiverTypeName` was
  exported for the receiver peel.
- **Commit is per-input, after the step runs.** `pendingWrite`/
  `pendingDecls`/`pendingMethods` are recorded at accept time and
  published in `commitWrites` (after `sealConsts`, on every success
  path); `rollbackSource` clears them so a failed input never
  half-publishes.
- **Decouple on `:unpin`/`:cd -`/another `:cd`.** Written names stay
  in the package (they're shared cells). Borrowed names drop from the
  repl scope; names the alias shadowed are restored — `pinPre` keeps
  the repl binding object itself, so the pre-pin value (and cell
  identity for anything that referenced it) survives the pin.
- **The shared-package risk stands, contained.** Patches are
  session-wide — every importer through this engine sees them — but
  the REPL runs on a fresh session engine, so the blast radius is one
  repl session. `:ls` marks published decls `patch` and bare `:cd`
  shows `[pin]` so the mode is visible.
- Command naming took the plan's `:pin` suggestion with the explicit
  pair `:unpin` (over `:edit`, which implies a different session model).

### Post-review fixes (first Devin Review pass)

- **`pinPre` snapshots → saved bindings.** Unpin used to copy the
  shared cell's *current* value into a private cell for names that
  existed before Pin — so `x := 1` + pin + `x = 5` + unpin left repl
  `x` at 5 and lost the original 1. Now `pinPre` stores the displaced
  repl binding object and Unpin hands it back: the loan ends, you get
  your own variable — and writes stay where they went (the package).
- **Aliases are loans, not `pending` globals.** A `x := v` alias was
  also recorded in `pending`, so a *failed* input (e.g.
  `x := oops()`) deleted the repl binding mid-session and every later
  `x = v` fell back to a repl-local raw Set — silently unpatching.
  Aliased names now skip `pending` (the alias predates the input;
  rollback only removes what the input created).
- **Published decls move packages.** A published func/type kept
  `Pkg = <repl>`, so its body resolved globals through the repl
  package — under `:cd` that worked via the pseudo dot-import, but
  `:cd -` removed it and the patch broke for every caller. Commit now
  retargets `Function.Pkg`/`TypeDef.Pkg` (and nested method Pkgs) to
  the entered package — the patch *is* a package member, SymbolID
  included. Side effect: a patch can no longer see unpublished repl
  scratch decls — surfaced immediately rather than silently breaking
  on Leave, which is the honest behavior for a moved decl.
- **The decl's file context travels with it.** The second half of the
  same bug: `resolveGlobalE` reads `pkg.Scopes[file]`/`Imports[file]`,
  and a replFile has no entry inside the entered package — grafted
  methods (typed `Pkg` correctly by `typeDefOf`) couldn't resolve a
  single repl import. `graftScope` registers the repl file's import
  table into the entered package at commit, skipping the self
  dot-import.
- **Method grafts reach live typedefs too.** A method on a
  `:pin`-declared type (`type T2 ...` then `func (t T2) M()`) has no
  index entry in the entered package — the graft now falls back to
  the published `*runtime.TypeDef` in `Globals`, via a shared
  `engine.methodFunc` helper extracted from `typeDefOf`.

## Round-3 notes: coverage audit — how much "where defined" survives

A decl-kind × metadata matrix measured against the real API
(testdata/inspectuse pins every row). "source" rows are packages
reached via `PackageOf`/`DirOf`/`FileOf`/`SourceOf`/`Current` — they
carry an index; "bound" rows are intrinsic-bound packages where the
index is nil and only `Globals` exist.

### Source packages — full metadata on every decl kind

| decl kind | Package / File / Pos / Doc | type detail | pinned by |
| --- | --- | --- | --- |
| func | all | `Signature` — param names, types, per-param Pos | SymbolView, SignatureWalk, FieldPos |
| method | all | `Signature` + `Recv` | MethodsWalk, SignatureWalk, DeclMeta |
| struct | all | `Def` → StructType; `Fields` → Names/Tag/Embedded/Pos + `Type` | FieldsWalk, FieldPos, CompositeFields |
| newtype (`MyInt`) | all | `Def` → base expr; `UnWrap`/`Origin` → underlying | TypeExprNav, OriginNav, NamedFieldType |
| `*newtype` (`PInt`) | all | `Def` → StarExpr; `UnRef`/`Origin` | OriginNav, NamedFieldType |
| interface | all | `Fields`/`MReqs`/`IEmbeds`; `~T` unions as children | IfaceMembers |
| var / const | all | declared type unreachable — `Def` only accepts TypeSpec (limitation); value via `Value` | DeclMeta, VarValueRead |
| generic decl (`Pair[T]`, `Reduce[T Number]`) | all | `TypeParams` → name + constraint (a named constraint keeps a resolvable SymbolID) | TypeParamsList |

Field-level type chasing works for Ident (same-pkg decl or
predeclared), SelectorExpr (via the file's import table), and every
composite node `Children` understands (MapType/ChanType/FuncType/
StructType/InterfaceType/arrays/pointers/unions).

### Bound packages — host pseudo-decls; `SourceOf` restores full info

| thing | direct (bound shadow) | via `SourceOf` |
| --- | --- | --- |
| func (`strings.Contains`) | `Symbol` → host decl: Kind/Name/Package + `Signature` synthesized from `BuiltinFunc.Target` (types only — no names, no Pos) | full decl: File/Pos/Doc + named params (SourceOfSrc) |
| type (`strings.Builder`) | host decl: Kind/Name/Package only — `Fields`/`Methods` trap, `Def` leaks `*runtime.TypeDef` | full decl: Fields/Methods/Pos/Signature from GOROOT source (SourceOfStruct) |
| method value (`r.Size`) | `SymbolOf` → host decl with `Package = nil` (ad-hoc builtin — owner lost); `PathOf` → nil | n/a — read the type's source-side method instead |
| intrinsic without `Target` (`strings.Compare`) | host decl; `Signature` traps | full signature from source |

Bound File/Pos/Doc are therefore reachable wherever real source
exists — through `SourceOf`, never through the bound shadow.

### Verified limitations (each pinned by a trap test)

- `Def`/`Fields`/`Methods`/`MReqs` on a non-type or host decl traps
  (TypeOfFuncTrap, MReqsStructTrap, DefVarTrap, BoundFieldTrap,
  BoundMethodTrap — asserted Go-side via `e.Run` errors).
- `Resolve`/`Origin`/`UnWrap`/`SameType` cannot descend into a bound
  package: the resolver consults the canonical package (no index) and
  traps — `no decl Builder in strings`. Field SymbolIDs such as
  `strings.Builder` still form correctly; chasing them needs a
  SourceOf-side lookup (ResolveBoundTrap).
- `SymbolID` on an instantiated type (`Pair[int]` → IndexExpr)
  returns nil — the generic origin isn't reachable; `Children` yields
  only the type arguments (Instantiation).
- `Symbol` on an unknown name traps (MissingSymTrap).
- `Signature` on an intrinsic without `Target` traps (HostSigTrap).
- Host method values lose their owner: `SymbolOf` reports Kind/Name
  with `Package = nil`, `PathOf` → nil (HostMethodSym).
- `State` reflects how the package was reached: a `DirOf`-only load
  stops at `indexed`; a `SourceOf` package stays `indexed` (never
  initialized); the subject package flips to `ready` once `Value`
  triggers init (SourceOfStruct, VarValueRead — order-dependent in
  the test list).

### What's still not pinned

`Import.Pos`/`Import.Name` aliasing (only Path is asserted), `Doc` on
files (`File.Doc`), and error message text (only trap occurrence is
asserted, not the message).
