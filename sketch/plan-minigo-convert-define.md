# Migrating `examples/convert-define` from `minigo` to `minigo`

> Status: conditions survey + migration plan. The `define` API itself is unchanged; this is an interpreter-swap under `internal/`.

## Goal

Replace every `minigo` (v1) dependency of `examples/convert-define` with `minigo`,
keeping the user-visible behavior identical: `convert-define -file <defs.go>` runs the
DSL file's `main`, intercepts `define.Convert` / `define.Rule` calls with **quoted**
(unevaluated) arguments, and produces `model.ParsedInfo` for `generator.Generate`.

convert-define has **no `symgo` dependency** — the interpreter is the only `minigo`
surface it uses, so a full minigo migration is possible.

## What convert-define actually uses from v1 `minigo`

`internal/interpreter.go` touches four v1 surfaces:

| v1 surface | Purpose |
|---|---|
| `minigo.NewInterpreter(scanner)` | build an interpreter bound to a `goscan.Scanner` |
| `interp.RegisterSpecial("path.Sym", handler)` | intercept `define.Convert` / `define.Rule` calls |
| `interp.LoadFile(file); interp.Eval(ctx)` | load the DSL file and run its `main` |
| handler ctx: `*evaluator.Evaluator`, `*object.FileScope`, `pos`, `args []ast.Expr` | inside the special: read quoted args, map import aliases → paths, scan packages for `scanner.TypeInfo` / `scanner.FunctionInfo` |

The handlers never *evaluate* the DSL: `define.Convert`'s `*ast.FuncLit` argument is
walked wholesale (`c.Map`/`c.Convert`/`c.Compute` calls are data, not dispatched calls),
and `dst *destination.DstUser` type expressions are resolved to `scanner.TypeInfo` via
`fscope.Aliases` + `Scanner().ScanPackageFromImportPath`.

## Condition table — v1 surface → minigo mechanism

| # | Requirement | minigo mechanism | Status |
|---|---|---|---|
| 1 | Run the DSL file's `main` | `Engine.Run(ctx, dir, "main")` loads a **directory package**; DSL files carry `//go:build codegen` and are filtered out without matching `BuildConfig.Tags` | **gap** → file-level entry added: `Engine.LoadFile` / `Engine.RunFile` (the file is the package, constraints ignored) |
| 2 | Register `define.Convert` / `define.Rule` as quoted calls | `Engine.RegisterSpecial(runtime.SymbolID{PackagePath, Name}, handler)`; compiler emits `OpSpecialCall` for `alias.Name` resolving to a registered `SymbolID` — the `define` package is never located or parsed | implemented (round 4) |
| 3 | Quoted arguments (`[]ast.Expr`, FuncLit kept as AST) | `runtime.QuotedCall.Call.Args` — args are never compiled/evaluated; handler gets `ctx.Position`, `ctx.Format`, `ctx.Errorf` | implemented (round 4) |
| 4 | Import alias → import path (`fscope.Aliases[ident]`) | `ctx.ResolveSymbol(expr)` maps `pkg.Sym` through the file's import table to `SymbolID{path, name}` — no materialization, and locals/upvals shadowing an import name are rejected | implemented (`ResolveSymbol`) |
| 5 | `pkg.Type` / `pkg.Func` → `scanner.TypeInfo` / `scanner.FunctionInfo` | **host side**: keep the `goscan.Scanner` built by `NewRunner` and call `ScanPackageFromImportPath(path)`. minigo's resolver is locator-level (`PackageMeta`) by design — it does not produce `scanner.TypeInfo` | no minigo change needed |
| 6 | `interp.Files()[0].AST.Name.Name` (package name of DSL file) | `pkg.Files[0].AST.Name.Name` on the `*runtime.Package` returned by `LoadFile` | implemented |
| 7 | `e.NewError(pos, ...)` | `ctx.Errorf(node, ...)` — position + message | implemented |
| 8 | `object.NIL` return from specials | `runtime.NIL` | implemented |
| 9 | `-tags` flag behavior | unchanged: the flag still only decorates generated output (`//go:build` header). The DSL file itself is loaded via `LoadFile`, which ignores build constraints for the named file | n/a |
| 10 | `main` as entry point | `Engine.Call(ctx, pkg, "main")` after `LoadFile` — same semantics as v1 `Eval` | via LoadFile |

## Decisions

- **File-level entry (`LoadFile`/`RunFile`) is the migration enabler.**
  The plan's entry model is directory packages (`minigo run ./app --entry F`), and
  `resolve.ReadPackageFiles` filters with `go/build` match rules. A DSL file guarded
  by `//go:build codegen` would need its tag mirrored into `BuildConfig.Tags` — brittle
  when the file sits in a directory with other non-DSL files. A named file is a
  self-contained package view: parse it, index it, run `main`. This matches v1
  `LoadFile` semantics exactly. Recorded as an out-of-plan addition in
  `plan-minigo-vm.md` (round-7 notes).
- **The `goscan.Scanner` stays on the host.** `model.StructInfo`/`TypeRule` are built
  from `scanner.TypeInfo`/`scanner.FieldType`; minigo deliberately has no scanner-level
  type API. `Runner.Scanner()` continues to serve `generator.Generate`.
- **`ResolveSymbol` over raw `Scopes`.** The alias→path lookup goes through
  `ctx.ResolveSymbol(expr)`: besides resolving `pkg.Sym` to a canonical `SymbolID`,
  it rejects locals/upvals that shadow an import name — a case a bare `Scopes`
  lookup would have silently misresolved.
- **No method special forms.** `c.Map`/`c.Convert`/`c.Compute` inside the quoted
  `FuncLit` are walked as AST by the `define.Convert` handler — plan §12.7's
  recommendation, unchanged.

## Remaining work (tracked in TODO.md)

- `SpecialContext.Resolve` / `ResolveType` — still unimplemented; convert-define is now
  the first real consumer that would exercise them (it currently does `ResolveSymbol` +
  host scanner itself).

## Verification

`internal/plan_test.go` (`TestConvertDefineSatisfiesPlan`) is the executable form of
the condition table: a spying `resolve.Resolver` on the real `Runner` proves zero
`Locate`/`LocateDir` calls, an aliased `d` import still dispatches as `SPECIAL_CALL`,
a dead `if false` branch never fires its special, and the `//go:build codegen` file
loads via `LoadFile`. `migration_guard_test.go` (`TestNoMinigoV1Dependency`) keeps v1
`minigo` out of the module's imports for good.
