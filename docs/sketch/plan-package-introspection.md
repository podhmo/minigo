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
| index | `Import`, `ImportDir`, `ImportFile`, `Current`, `Members`, `Decls`, `Files`, `Imports` | locate + parse + index | none |
| syntax | `Doc`, `Pos`, `Fields`, `Signature`, `TypeExpr` views, `SymbolID`, `Resolve`, `SameType`, `UsedSymbols` | AST reads + import-table lookup | none |
| value | `Value`, `TypeOf`, `Kind`, `Methods` | materialize | `Value` on var/const runs `EnsureReady` (package init) |

The value layer is deliberately a small, explicit annex — reaching a
`var`/`const` member must be an opt-in (`Value`) because it may run
init side effects. `TypeOf`/`Kind`/`Methods` materialize decls only
(functions and typedefs are init-free even under the default
`GoCompatibleInit`).

## Script API sketch

```go
import "minigo.dev/inspect"   // stub package; engine binds intrinsics

p := inspect.Import("strings")      // fake import: loadPath -> Indexed
q := inspect.ImportDir("./app")     // dir entry point
f := inspect.ImportFile("./schema.go") // single-file package (Engine.LoadFile)
self := inspect.Current()           // caller's *runtime.Package

inspect.Name(p) / Path(p) / Dir(p) / State(p)
inspect.Members(p)                  // []Symbol — index-level, no init
inspect.Symbol(p, "Contains")       // one decl
inspect.Decls(f)                    // decls declared in one file (schema file
                                    // reads as: imports + this file's decls)
inspect.Files(p)                    // []File{Name, Imports[{Path, Name, Pos}], Doc}
inspect.Imports(f)                  // the file's own import table
inspect.SymbolID(s)                 // {PackagePath, Name}

// syntax layer — s is a Symbol (decl view)
inspect.Kind(s)                     // "func"|"method"|"var"|"const"|"type"|"host"
inspect.Doc(s); inspect.Pos(s)      // doc comment text; "file.go:12:6"
inspect.Fields(s)                   // struct type -> []Field
inspect.Signature(s)                // func/method -> {Recv, Params, Results}
inspect.TypeParams(s)               // generic decl's type parameter fields
```

`Field` is a view over `*ast.Field` + the declaring file:

```
Field{ Names []string, Type *TypeExpr, Tag string, Doc string, Embedded bool, Pos }
TypeExpr{ Text string /* format.Node */, Kind string /* "Ident", "SelectorExpr",
          "StarExpr", "ArrayType", "MapType", "ChanType", "FuncType", ... */ }
inspect.Children(te)  // []TypeExpr — drill into composite exprs:
                      // []*db.User -> *db.User -> db.User
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
terms — e.g. a `[]*db.User` field: Children twice, SymbolID gives
`{db, User}` for free, Resolve opens db's decl, Fields repeats.

## Consumer story: scaffolding generation (convert-define)

The concrete dream: a minigo script that emits the *definitions*
file `examples/convert-define` expects — pairing decls across two
packages by name and printing Go fragments. The inspect layer covers
each step:

```go
src := inspect.Import("myapp/model")      // e.g. db models
dst := inspect.Import("myapp/api")        // e.g. DTO structs

for _, s := range inspect.Members(src) {
    if s.Kind != "type" { continue }
    d := pairFor(s, inspect.Members(dst))  // script-side rule:
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

- View structs (`Symbol`, `File`, `Field`, `TypeExpr`, `Import`) are
  boxed as `*runtime.GoValue`: exported field access already works via
  the existing reflective member dispatch, and internals (raw
  `ast.Node`s) stay out of the FFI — `TypeExpr` keeps `ast.Expr` +
  declaring file as hidden context for `SymbolID`/`Resolve`.
- `Import`, `Current`, `Symbol`-targeting and `Value`/`TypeOf` return
  real runtime values (`*runtime.Package`, `*TypeDef`, `*Function`,
  `*runtime.SymbolID`-shaped struct) — they compose with everything a
  script can already do (`p.Sym`, `f(args)`, `T{...}`).
- Accept `*runtime.ImportRef` anywhere a package is expected, so
  `p := fmt; inspect.Members(p)` works on a source import.

## Bound packages

`Bind`-registered packages have no index. `Members` falls back to
`Globals.Names()` yielding `Kind:"host"` symbols (no Pos/Doc), so
stdlib intrinsics introspect uniformly with source packages.

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
- `:ls [name]` — `Members` of the current package, or `Fields`/`Signature`
  of a named symbol. CLI-side sugar over the same inspect intrinsics.

## Implementation notes

- `inspect/inspect.go` — stub declarations (host-package pattern);
  bound under both `minigo.dev/inspect` and the repo path.
- `inspect.go` (root) — the `Bind` table + view mapping. Needs engine
  internals (`loadPath`/`loadDir`, `materialize`, `fset` for
  positions, declaring-file import tables for `SymbolID`), so it
  installs in-process like `installStdlib`, not via `gen-intrinsics`.
- `VMCaller` grows `Package() *runtime.Package` (caller's package) to
  implement `Current` as an ordinary builtin.
- `runtime.ImportRef` gains an internal flag (e.g. `AllNames`) used by
  the REPL pseudo-import to skip the exported-name gate in
  `resolveGlobal`'s dot-import walk.
- `UsedSymbols` shares the same walk: per file, collect
  `SelectorExpr`s whose `X` resolves to an import-local name and map
  each through the import table to `SymbolID` — index-time work, no
  evaluation.

## Open questions

- Package name: `minigo.dev/inspect` vs folding into `host` vs
  `minigo.dev/pkg`. `inspect` reads best for a browse-only surface.
- `SameType` semantics: SymbolID-equality is strict (declared-type
  identity). A looser mode (underlying-shape equality, alias
  transparency) may be needed for real codegen — start strict.
- Should `inspect.Import` accept a file path too (unify `ImportFile`),
  or keep file/dir/path as three explicit entries?
- Interface decls: expose `MReqs`/`IEmbeds` as `Fields`-like views?
  Probably a `Methods(sym)` answer later.
- Whether `:cd` ever gains a write mode (`:pin`/`:edit`) for
  session-scoped patching of a package's globals.
