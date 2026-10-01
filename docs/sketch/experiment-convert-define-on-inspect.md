# Experiment: convert-define running entirely on minigo inspect

Status: experiment (draft, not necessarily for merge)

Question: can `examples/convert-define` — which vendored ~2000 lines of
go-scan (`pkg/goscan`, `pkg/scanner`, `pkg/locator`) to turn quoted
`define.Convert`/`define.Rule` calls into conversion code — be rewritten
on top of minigo's own lazy package loading and the `inspect` view layer?
And where does that surface still fall short?

## Verdict

Yes. The vendored `pkg/` tree is deleted; every capability it provided is
replaced by pieces the interpreter already has. `make e2e` regenerates
`e2e_test/generated.go` byte-identically, and all unit tests pass —
including the plan-§12 laziness acceptance test, updated to the stronger
claim "exactly the packages the DSL names are located" (see
`internal/plan_test.go`).

The one wholesale gap: **nothing script-side can walk a function body** —
and this experiment never needed it. The `define.Convert(func(c, dst, src)
{ ... })` mapping DSL is read by the host special-form handler
(`mappingWalker` over the `*ast.FuncLit`), which was always the design:
the special form is the interface, not something `inspect` is missing.

## What the vendored packages did → what replaced them

| vendored go-scan | replacement |
|---|---|
| `goscan.Scanner` (module-aware package loader over a workdir) | `engine.Package(ctx, path)` — the engine's own Locate→Parse→Index pipeline, anchored at the define file's directory. Strictly better scoping: resolution follows the file, not the process cwd |
| `ctx.ResolveSymbol` + `ScanPackageFromImportPath` per quoted `pkg.Type` arg | `xinspect.NewTypeExpr(expr, ctx.File(), ctx.Package())` + `SymbolID()` → `{import path, name}` without loading; `lookupDecl` (via `engine.Package`) pays the one-package cost |
| `scanner.TypeInfo` (struct model: name, fields, kind) | `xinspect.Decl` + `xinspect.FieldsOf` (`[]*Field` with `Names`, `Type`, `Tag`, `Embedded`) + `xinspect.DefOf` (`Kind == "StructType"`) |
| `scanner.FieldType` (resolved type graph: IsPointer/IsSlice/IsMap/Elem/MapKey/TypeArgs/FullImportPath/Definition/Resolve) | `*xinspect.TypeExpr`: `Kind` (ast node name), `Children()` (composite parts), `Unref()` (pointer peel), `SymbolID()` (canonical identity), `Resolve`/`Unwrap`/`Origin` (lazy decl chasing through an `xinspect.Resolver`) |
| `ExternalTypeOverride` for `time.Time` | bound packages carry no `Index`; `lookupDecl` synthesizes `xinspect.NewHostDecl` for their members — same "known to exist, not introspectable" semantics, no registration table needed |
| `scanner.ResolveType` eager cross-package resolution pass | gone — `TypeExpr` resolves on demand through `runner.TypeResolver()` (`SymbolID → engine.Package → Index.Types`), so only the packages actually touched are loaded |
| `TypeInfoFromExpr` AST→model translation (832-line scanner.go) | gone — `TypeExpr` *is* the type model; `model/typeref.go` adds the three shapes the generator needs (`TypeKey`, `ResolveNamed`, `IsStructDecl`, `StructElemOf`) |
| `goscan.ImportManager` | moved to `generator/importmanager.go` — it never depended on the scanner (its constructor took a `PackageInfo` just for one string) |
| `pkg/locator` (module/replace/GOPATH walking, ~500 lines) | root `resolve` package (`resolve.GoScanResolver`) — the vendored copy was a stale duplicate of it |

## Gaps found in `inspect` / the engine

Ordered by how much they hurt this consumer.

### 1. `TypeExpr` can't re-wrap sub-expressions in its own context (real gap)

`TypeExpr.expr/file/pkg` are unexported and `Children()` only covers a
curated set of children. Two concrete losses for codegen:

- **`IndexExpr`/`IndexListExpr` children are only the type arguments** —
  the base `X` (the `List` in `pkg.List[T]`) is not a child, so the
  generic type's declaring package is unreachable: no `SymbolID`, no
  import registration, `Text` keeps the file's local alias (which may
  not exist in the generated file). Generic-typed fields are silently
  misrendered.
- **Array length / channel direction** exist only inside `SameType`'s
  internals (`sameShapeExtra`): `[3]int` renders as `[]int`, `<-chan T`
  as `chan T`. (The previous implementation made the same slice/array
  conflation, so e2e parity holds — but it's a view-level blind spot.)

A `(*TypeExpr).Sub(ast.Expr)` (the existing unexported `withExpr`) or a
`File()`/`Package()` accessor on `TypeExpr` closes the whole class:
`Expr()` already hands back the raw AST, so callers could rebuild
sibling views for `IndexExpr.X`, `ChanType.Dir`, `ArrayType.Len`.

### 2. No host-side `SourceOf` (bound-shadowed stdlib decls)

`engine.sourceOf` (the code behind `inspect.SourceOf`) is unexported, so
host code can't reach the real `time.Time` decl behind the bound `time`
package — hence the `NewHostDecl` pseudo-decl workaround in
`lookupDecl`, which is exactly what `ExternalTypeOverride` was. An
exported `engine.SourceOf(path)` (or a `SourceOf` flag on
`engine.Package`) would let host tools see through bound shadows the way
scripts can.

### 3. `inspect.Sig` boxes fields for the script FFI — awkward host-side

`SignatureOf` returns `Params`/`Results` as `*runtime.Slice` of
`*runtime.GoValue`, so host callers must unbox (`gv.V.(*xinspect.Field)`).
A `[]*xinspect.Field` accessor (parallel to `FieldsOf`) would make the
signature story symmetric with the fields story.

### 4. Smaller things

- `index` only indexes top-level decls; type decls inside function
  bodies are invisible (the vendored scanner picked them up). Nobody
  converts on body-local types in practice.
- `inspect.Field` doesn't expose `IsExported`/computed names for
  embedded fields — trivially derivable, but a convenience.
- A canonical `TypeExpr` identity string (`TypeKey` in `model/typeref.go`
  hand-rolls `"*path.Name"`) might belong in inspect itself — codegen
  consumers will all re-implement it.

### 5. Ergonomics, not gaps

- A nil-pointer panic inside a special-form handler surfaces as a bare
  `runtime error` with no Go stack — debugging the rewrite needed a
  temporary `debug.Stack()` patch in `vm.asError`. A trap carrying a
  stack (or the panic value's origin) would help every future host
  extension author.

## Bugs fixed along the way

- **Cross-module rule matching was silently broken**: the rule table key
  used `FieldType.String()` (the *package name* spelling, e.g.
  `time.Time`) while `findMatchingRule` compared it against
  `PkgPath`-qualified names (`example.com/m/pkg.T`). They coincide for
  stdlib but never for module paths, so `define.Rule` could only ever
  match stdlib/builtin types. `TypeKey` now renders both sides as
  canonical `{import path}.{name}`.
- **`FieldInfo.JSONTag` was populated nowhere** — the "normalized json
  tag" match priority in the generator was dead code. `ensureStructInfo`
  now fills it from `inspect.Field.Tag`, making the priority real.

## What this suggests about `inspect`

The sketch's "Consumer story" holds up: decl-granular, laziness-preserving
views are sufficient for a real code generator — with *less* code than the
scanner needed (the AST→model translation layer evaporates; `TypeExpr` is
the model). The worthwhile additions, in priority order:

1. `(*TypeExpr).Sub`/`withExpr` export (or `File()`/`Package()`), so
   `IndexExpr` bases, chan direction and array length are reachable.
2. A host-callable `SourceOf` (export `engine.SourceOf`), so bound
   shadows stop being opaque for tools.
3. A host-friendly `[]*Field` signature accessor.
4. A canonical identity string on `TypeExpr` (the `TypeKey` role).

None of these is a new *feature* in the sense of new machinery — they're
exports/accessors over structures that already exist.
