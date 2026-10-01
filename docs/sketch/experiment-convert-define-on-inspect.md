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
including the plan-§12 laziness acceptance test, asserting the stronger
claim "exactly the packages the DSL names are located" (see
`internal/plan_test.go`).

The four host-side gaps this experiment found in `inspect`/the engine
(`Sub`, `SourceOf`, `Sig` field accessors, `CanonicalName`) all landed on
main as #20 — this branch was rebased onto them and the workarounds
(`sigFields` unboxing, `model.TypeKey`, the `NewHostDecl`-only fallback
comment) removed. The one panic-stack item turned out to be already
landed by #17 (`runtime.Panic.GoStack`).

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
| `scanner.FieldType` (resolved type graph: IsPointer/IsSlice/IsMap/Elem/MapKey/TypeArgs/FullImportPath/Definition/Resolve) | `*xinspect.TypeExpr`: `Kind` (ast node name), `Children()` (composite parts), `Unref()` (pointer peel), `SymbolID()`/`CanonicalName()` (canonical identity), `Resolve`/`Unwrap`/`Origin` (lazy decl chasing through an `xinspect.Resolver`), `Sub()` (any sub-expr, incl. generic bases) |
| `ExternalTypeOverride` for `time.Time` | bound packages carry no `Index`; `lookupDecl` synthesizes `xinspect.NewHostDecl` for their members — same "known to exist, not introspectable" semantics, no registration table needed (deliberate — see "SourceOf" below) |
| `scanner.ResolveType` eager cross-package resolution pass | gone — `TypeExpr` resolves on demand through `runner.TypeResolver()` (`SymbolID → engine.Package → Index.Types`), so only the packages actually touched are loaded |
| `TypeInfoFromExpr` AST→model translation (832-line scanner.go) | gone — `TypeExpr` *is* the type model; `model/typeref.go` adds only `ResolveNamed`/`IsStructDecl`/`StructElemOf` (`TypeKey` moved into inspect as `CanonicalName`) |
| `goscan.ImportManager` | moved to `generator/importmanager.go` — it never depended on the scanner (its constructor took a `PackageInfo` just for one string) |
| `pkg/locator` (module/replace/GOPATH walking, ~500 lines) | root `resolve` package (`resolve.GoScanResolver`) — the vendored copy was a stale duplicate of it |

## Gaps found — all landed on main

Ordered by how much they hurt this consumer. All four were filed as
issue #19 and landed as #20; this branch uses every one.

### 1. `TypeExpr` couldn't re-wrap sub-expressions in its own context → `Sub` (#20)

`TypeExpr.expr/file/pkg` are unexported and `Children()` only covers a
curated set of children, so `IndexExpr`/`IndexListExpr` bases
(`pkg.List` in `pkg.List[T]`), `ArrayType.Len`, and `ChanType.Dir` were
unreachable — generic-typed fields got no import registration and kept
the file's local alias in generated code.

`withExpr` is now exported as `(*TypeExpr).Sub`: host code re-wraps any
sub-expression in the same context. The generator registers the generic
base's package and renders `pkg.List[int]` through `im.Qualify`.
`ChanType.Dir`/`ArrayType.Len` need no child view (not `ast.Expr`s
worth resolving) — `Expr()` already exposes them.

### 2. No host-side `SourceOf` → `Engine.SourceOf` (#20) — landed, and deliberately unused here

`engine.sourceOf` is exported as `Engine.SourceOf(ctx, path)`: host code
can now reach the real decl behind a bound package's shadow.

**The interesting finding is that convert-define should NOT use it.**
Wiring `SourceOf` into `lookupDecl` was tried on this branch and
reverted: bound-stdlib opacity is the correct *consumer* policy —
`ExternalTypeOverride`'s semantics all along. A real `time.Time` decl
would:

- **cost laziness**: `SourceOf` parses and indexes GOROOT source for
  every bound member the DSL touches (plan_test's located-set grew `time`
  before the revert).
- **lie about struct-ness**: `IsStructDecl(time.Time)` becomes true, so
  a same-type `*time.Time -> *time.Time` field routes into a
  nonexistent `convertTimeToTime` call instead of the pointer-copy path.
- **introspect what codegen can't use**: stdlib internals are unexported
  and unassignable from generated code regardless.

The API is right for tools that need real decls (doc generators, linters
on bound packages); for codegen, `NewHostDecl` pseudo-decls remain the
correct answer — "known to exist, opaque on purpose".

### 3. `inspect.Sig` boxed fields for the script FFI → `ParamFields`/`ResultFields` (#20)

`SignatureOf` still returns `Params`/`Results` as `*runtime.Slice` (the
script FFI shape — changing it would break scripts), but the new
`sig.ParamFields()`/`ResultFields()` unbox to `[]*Field` for host
callers. convert-define's hand-rolled `sigFields` is deleted.

### 4. No canonical identity on `TypeExpr` → `CanonicalName` (#20)

`model/typeref.go`'s `TypeKey` (`"*import/path.Name"` for named types,
builtin name for predeclared, `""` for composites) moved into inspect as
`(*TypeExpr).CanonicalName()` — generalized to host-backed exprs
(`reflect:ptr` peels like `StarExpr`; unnamed host composites report
`""`). `model.TypeKey` is deleted; rules and map keys use the method.

### 5. Panics inside host handlers lose their stack — already landed by #17

A nil-pointer panic inside a special-form handler used to surface as a
bare `runtime error`. An earlier revision of this report claimed the Go
stack was still dropped after #17's `asScriptPanic` — **that was wrong**:
`asScriptPanic` stores `debug.Stack()` into `runtime.Panic.GoStack` and
`Panic.Error()` renders it. Host-extension authors now get both the
script frames (which DSL call panicked, with its source line) and the Go
stack (which line inside the handler panicked).

## Smaller things (still open)

- `index` only indexes top-level decls; type decls inside function
  bodies are invisible (the vendored scanner picked them up). Nobody
  converts on body-local types in practice.
- `inspect.Field` doesn't expose `IsExported`/computed names for
  embedded fields — trivially derivable, but a convenience.

## Bugs fixed along the way

- **Cross-module rule matching was silently broken**: the rule table key
  used `FieldType.String()` (the *package name* spelling, e.g.
  `time.Time`) while `findMatchingRule` compared it against
  `PkgPath`-qualified names (`example.com/m/pkg.T`). They coincide for
  stdlib but never for module paths, so `define.Rule` could only ever
  match stdlib/builtin types. Canonical `SymbolID`-backed names
  (`CanonicalName` now) render both sides identically.
- **`FieldInfo.JSONTag` was populated nowhere** — the "normalized json
  tag" match priority in the generator was dead code. `ensureStructInfo`
  now fills it from `inspect.Field.Tag` (after fixing a double-quote
  bug: `Field.Tag` is already unquoted, so `reflect.StructTag` takes it
  directly), making the shared-json-tag priority real — pinned by
  `TestIntegration_GenericAndJSONTag` (`src json:"user_id"` → `dst
  json:"user_id"` auto-maps `ID -> UserID`).

## What this suggests about `inspect`

The sketch's "Consumer story" holds up: decl-granular, laziness-preserving
views are sufficient for a real code generator — with *less* code than the
scanner needed (the AST→model translation layer evaporates; `TypeExpr` is
the model). Every missing piece was an export/accessor over structures
that already exist, not new machinery — and one of them (`SourceOf`)
turned out to be an API this consumer correctly declines: the right
boundary is that codegen treats bound stdlib as opaque even when the
real decl is reachable.
