# Plan: Re-architecture of minigo (post-v1 consolidation)

> **Status**: proposal. Written from the viewpoint of a code owner who may
> freely restructure packages and reshape the VM, while keeping the project's
> founding ideas intact. Snapshot: main `a3f4e24` (2026-11-17).
>
> Related: [plan-minigo-vm.md](./plan-minigo-vm.md) (the original design),
> [ja/todo-priority.md](./ja/todo-priority.md),
> [ja/experiment-gosimilarity-refactoring.md](./ja/experiment-gosimilarity-refactoring.md),
> [experiment-frame-path-perf.md](./experiment-frame-path-perf.md).

## 0. What must not change

These are the project's identity. Every step below is checked against them.

| # | Invariant | Source |
|---|---|---|
| I1 | **Lazy Go** — a package is located/parsed/indexed/initialized only when execution reaches it. No `go/types`, no `go/packages`, no `go list`. | plan-minigo-vm §1, §4, §5; AGENTS.md |
| I2 | **Total compiler** — the compiler never rejects an AST; unsupported constructs become `TRAP` that fires only if executed. | plan-minigo-vm §3 |
| I3 | **Scripts are valid Go** — gopls/vet work on scripts; interpreter extensions are stub packages (`minigo.dev/host`, `define`). | plan-minigo-vm §1, §11 |
| I4 | **Quoted Go** — special forms dispatch by canonical `SymbolID` at run time (`SPECIAL_CALL` + quote table), never AST→AST macros, and never load the stub package. | plan-minigo-vm §12 |
| I5 | **Never pretend success** — a silent wrong answer is worse than a trap. | ja/todo-priority.md |
| I6 | **Speed is a non-goal** — simplicity and coverage beat run-time speed; perf work is justified only when it also simplifies or removes a measured pathology. | README |
| I7 | **Public embedding API stays source-compatible** — `NewEngine`, `Run`, `Call`, `Bind`, `RegisterSpecial`, `WrapFunc`, `ValueOf`, `inspect`, `repl`, `storybook` users (all `examples/*`) keep compiling, or get a mechanical migration. | examples/ |

Everything else — package boundaries, the VM's frame/stack layout, the type
model, the intrinsic table's shape — is open.

## 1. Diagnosis

### 1.1 Size and shape

| File | Lines | Notes |
|---|---:|---|
| `vm/vm.go` | 19,093 | interpreter loop + scheduler + host FFI + untyped constants + type assertion/iface satisfaction + generics inference + special-form context |
| `intrinsics.go` | 11,033 | `installStdlib` alone is ~3,000 lines (one function) binding ~60 stdlib packages |
| `compile/compile.go` | 7,295 | scopes, stmts, exprs, const folding, static-type hints, goto checks, composite literals, specials |
| `runtime/value.go` + `typedef.go` + `package.go` | 5,662 | values, `TypeDef`, package lifecycle, `SpecialContext`, `VMCaller`, panics/traps — all in one package |
| `minireflect/` | 6,766 | reflect facade for script values |
| root `minigo` | ~18,000 | Engine + builtins + dispatch (method sets) + inspect + iter.Seq + host vars + embed + intrinsics |

The original layout (plan-minigo-vm §14: `loader/`, `types/`, `builtin/`,
`intrinsic/`, `special/`, `ffi/`, `debug/`) was never materialized; its
responsibilities accreted into `vm.go`, `runtime/` and the root package.

### 1.2 Structural problems (in order of cost)

1. **There is no single type model.** `runtime.TypeDef` is both a *lazy AST
   reference* (`Spec`, `Anon`, `EmbedSpecs`, `IEmbeds`, `TConstraints` as
   `ast.Expr`) and a *resolved type* (`Elem`, `Embeds`, `Methods`, `Binds`,
   `HostIdent`, `HostNew`, `HostScalar`). Identity is therefore approximated
   by many heuristics spread over the VM: `tdShapeEq`, `sameSpelledTwin`,
   `hostNilTwin`, `hostTwinIs`, `peelAlias`, `structFieldsEq`,
   `underlyingShape`, `typeExprFor`, `reflectTypeExpr`, … The gosimilarity
   report found the "`td.Anon` else `td.Spec.Type`" idiom open-coded in 18
   places and TypeDef construction duplicated in 4. Most correctness bugs in
   TODO.md (named-vs-host twins, alias identity, promoted fields on host
   embeds) live at this seam.
2. **Static type knowledge is split between the compiler and the VM.** The
   compiler computes partial static types ad hoc (`staticOpTyp`,
   `callOpTyp`, `stampVarTyps`, `opTyp`) and ships them to the VM as
   `OpLocalTyp`/`OpGlobalTyp` stamps and per-call `statics []*TypeDef`
   (50 uses in vm.go), which the VM uses for inference and untyped-constant
   defaulting. There is no named contract for *what* the compiler may know.
3. **The laziness contract at compile time is implicit.** The compiler calls
   `ImportRef.Materialize()` (14 sites in `compile/` and `vm/`) to type
   operands such as `pkg.Name`. That parses and indexes a package referenced
   anywhere in the function being compiled, including branches never taken.
   It never initializes, so I1 holds in spirit, but the rule ("compile may go
   up to *Indexed*, never *Initializing*") is nowhere written and nothing
   enforces it.
4. **The VM is a god object.** `*VM` owns the dispatch loop, frames, defer/
   panic/recover, goroutine scheduling and deadlock detection (`proc`,
   `gwait`, `parkWake`), the reflect boundary (`toReflectValueDepth`,
   `goValueOf`, `hostMarshal`, `adaptFunc`), the script→host interface
   adapters (`scriptIface` implementing `io.Reader`, `fs.FS`,
   `sort.Interface`, …), untyped-constant arithmetic (`UConst`,
   `constToBasic`, `scalarConst`), interface satisfaction, generics inference
   (`inferCached`, `unifyIndices`, `checkTArgs`) and `specialCtx`. Each of
   these is a separate concern with its own tests elsewhere.
5. **`VMCaller` is a 21-method interface** that every builtin and intrinsic
   depends on, so nothing can move out of the root package or `vm` without
   dragging it along.
6. **The intrinsic table regrew into per-package bindings** — the very
   thing plan-minigo-vm §11 wanted to collapse. That was the right call in
   practice (experiment-oapi-codegen-perf: "running work natively was the
   only lever worth more than ~7%"), but the shape is a 3,000-line function
   with no per-package ownership, no per-package tests and no generated
   parts despite `minigo gen-intrinsics` existing.
7. **Frame/locals layout is pessimistic by construction.** Every local is a
   heap `*Cell`; `OpLocalRef` is emitted for every store target, so
   `chunkCellsEscape` is true for almost every chunk and the cell-block reuse
   (#1024) rarely applies; each frame grows its own operand stack. This is a
   *design* issue (the compiler does not classify slots), and the perf TODOs
   are its symptoms.

## 2. Target architecture

### 2.1 Layers

```text
                ┌──────────────────────────────────────────────┐
  consumers     │ cmd/minigo  repl  storybook  inspect  examples│
                └───────────────────────┬──────────────────────┘
  facade        minigo (Engine, Option, Run/Call/Bind/RegisterSpecial,
                        WrapFunc/ValueOf — thin, re-exports)
                                        │
  host side     stdlib/…  (bound packages)      special/  (registry,
                hostffi/  (reflect boundary,     QuotedCall, Context impl)
                           script→host adapters)
                builtin/  (len, append, make, …)
                                        │
  execution     vm/        (dispatch loop, frames, calls, defer/panic)
                vm/sched/  (goroutines, park/wake, deadlock detection)
                                        │
  compile       compile/   (AST → bytecode; slot classification;
                            static hints through types.Oracle)
                bytecode/  (ops, Chunk, CallSite descriptors, verifier, disasm)
                                        │
  semantics     types/     (Type: resolved + interned; TypeRef: lazy;
                            method sets, iface satisfaction, conversion
                            rules, generics inference/instantiation)
                constant/  (untyped constants: UConst arithmetic, defaulting)
                                        │
  object model  runtime/   (data only: Value kinds, Cell, Struct, Slice, Map,
                            Chan, Function, Closure, Panic, Trap)
                                        │
  front         loader/    (Package lifecycle state machine, ImportRef, Env)
                index/  syntax/  resolve/  pkg/locator
```

Rules:

- **Imports point downward only.** Enforced by `archtest` (§4, phase 0).
- **`runtime/` holds data, not algorithms.** Struct definitions and trivial
  accessors stay (Go forbids cycles, so `Named → TypeDef → Function → Chunk`
  must live together); resolution, satisfaction, inference and conversion
  logic move up into `types/` and `constant/`.
- **`vm/` knows nothing about `reflect`.** Host values are opaque
  `*runtime.GoValue` to the loop; every host interaction goes through the
  `hostffi` interface the VM is constructed with.

### 2.2 Capability interfaces instead of `VMCaller`

Split the 21 methods into small interfaces, each implemented by the VM and
accepted by the narrowest consumer:

```go
package runtime

type Caller interface   { Call(fn Value, args []Value) (Value, error); Recover() Value }
type Spawner interface  { Spawn(fn Value, args []Value) *Task; Task() *Task }
type TypeOps interface  { TypeOf(Value) *TypeDef; Zero(*TypeDef) Value; Convert(*TypeDef, Value) (Value, error); Equal(*TypeDef, Value, Value) bool; Copy(Value) Value }
type Members interface  { Member(base Value, name string) (Value, bool); MethodSetOf(Value) (map[string]bool, bool) }
type Frames interface   { CallerPCs() []uintptr; CallerFrame(pc uintptr) (CallSite, bool) }

// VMCaller stays as the union for source compatibility during migration.
type VMCaller interface { Caller; Spawner; TypeOps; Members; Frames; /* … */ }
```

A `strings` intrinsic then needs `Caller` at most; most need nothing. This is
the precondition for moving bindings out of the root package.

### 2.3 One type model: `types.Type` + `types.Ref`

```go
package types

// Ref is a lazy, unresolved type expression in a lexical context.
// Indexing produces Refs; nothing is loaded to build one.
type Ref struct { Expr ast.Expr; Scope *Scope }   // Scope: file imports + local types + generic binds

// Type is a resolved type. Types are interned per Universe: identity is
// pointer equality, so all *Twin/*Shape heuristics collapse into ==.
type Type struct {
    Kind    Kind
    Named   *NamedInfo   // pkg path + name + type args (nil for unnamed)
    Elem    *Type; Key *Type; Len int64
    Fields  []Field      // resolved lazily, once (embedding path precomputed)
    Methods *MethodSet   // lazily built once; shared by assertion and dispatch
    Host    reflect.Type // non-nil when the type *is* a host type
    Def     *runtime.TypeDef // back-pointer during migration
}

type Oracle interface {
    // Resolve may advance packages up to Indexed, never to Initializing.
    Resolve(Ref) (*Type, error)
}
```

- Host types get exactly one `*Type` per `reflect.Type`, and a bound named
  type (`time.Duration`) and its source declaration resolve to the *same*
  `*Type` — eliminating the twin concept rather than adding another twin
  check.
- `MethodSet` is computed once per type and serves `MethodSetOf`,
  `MethodFuncsOf`, interface satisfaction and promoted-member lookup (fixes
  the "walks the method set twice" TODO structurally).
- Selector resolution caches the *field index path* per `(Type, name)`
  (fixes "selector resolution re-searches by name").
- `runtime.TypeDef` survives as the declaration record (what index/compile
  produce); `TypeDef.Type()` lazily yields the interned `*Type`. Migration
  is call-site by call-site, never a flag day.

### 2.4 The compile-time laziness contract, written down

> **C1.** Compiling a function may advance a package referenced *syntactically
> in that function* up to `Indexed`. It must never run `Initialize`.
>
> **C2.** All such advances go through a single `types.Oracle` owned by the
> compiler; no direct `ImportRef.Materialize()` in `compile/`.
>
> **C3.** If the oracle cannot answer without violating C1 (or fails), the
> hint is *unknown* and the VM decides dynamically. A missing hint must never
> change semantics in a way gc would not (I5): unknown is always safe.

`compile/typehint.go` then replaces `staticOpTyp`/`callOpTyp`/`opTyp`/
`stampVarTyps`. Its output is per-instruction static information stored in
the chunk (see 2.5), not a side channel of `OpLocalTyp` pushes and
per-call `statics` slices.

An opt-in `WithStrictLazyCompile()` (oracle refuses anything not already
`Indexed`) lets tests prove C3: running the conformance suite with it must
give identical results, only slower or with more dynamic checks.

### 2.5 VM shape

Changes, each independently landable and measured:

1. **Slot classification at compile time.** Each local is `Plain` (value in
   the frame's slot array) or `Boxed` (`*Cell`; captured by a closure,
   address-taken by `&x`, or exposed to a special form's `Eval` scope).
   Store targets get direct ops (`OpSetLocal`, `OpSetLocalIndex`,
   `OpSetLocalField`) instead of `OpLocalRef` + generic ref stores. This
   removes the double allocation of non-param locals, makes
   `chunkCellsEscape` precise, and *simplifies* `assignRef`.
2. **One contiguous value stack per goroutine.** Frames are windows
   `(base, sp)` into it; `Chunk.MaxStack` comes from the bytecode verifier.
   Arguments are passed in place (no `popArgs` slices, no receiver-prepend
   copy for bound methods).
3. **`CallSite` descriptors in the chunk.** Static argument typedefs,
   spread element typedef, special-form quote index and the source position
   become one immutable `bytecode.CallSite` referenced by `OpCall`'s operand.
   `frame.argbuf/statbuf` and `deferredCall.statics/spreadTd` disappear.
4. **Inline caches in `CallSite`/`Site` slots** (atomic pointer, so shared
   chunks stay race-free): `OpGlobal` caches the resolved cell guarded by an
   `Env` epoch that `:pin`/rebinding bumps; `OpSelect` caches
   `(*Type → access path)`. This is the "later `OpSelect` inline cache" of
   plan-minigo-vm §16, now possible because types are interned.
5. **Bytecode verifier** (`bytecode.Verify`) run in tests and under a debug
   option: stack-effect consistency, jump targets, slot kinds. It turns a
   class of compiler bugs into immediate test failures and produces
   `MaxStack`.
6. **Keep**: `Value = any` with boxed composites, `Tuple` for multi-results,
   `Trap` vs `Panic`, per-function lazy compilation, `SPECIAL_CALL`. A tagged
   `Value` struct is explicitly **out of scope** (I6; the profile is flat and
   native binding is the proven lever).
7. **Retire `OpEvalAST`** except as the REPL/`EvalExpr` bridge; rename it to
   `OpEvalFragment` and document it as that, since its migration role from
   v1 is over.

### 2.6 Host boundary as a package: `hostffi/`

Moves out of `vm.go` (roughly lines 3,950–8,600 and 12,000–14,400 today):
`goValueOf`, `toReflectValueDepth`, `hostMarshal`, `adaptFunc`,
`hostArgAs`, opaque stores, host nil/twin helpers, and the `scriptIface`
family (`io.Reader`, `io.Writer`, `io.Seeker`, `fs.FS`, `fs.File`,
`fs.FileInfo`, `fs.DirEntry`, `sort.Interface`, `fmt.Stringer`, …).

The adapter family is generated from a table (`interface → methods → arity`)
rather than hand-written per combination (`scriptFileSeek`,
`scriptFileSeekWT`, `scriptDirFileSeekWT`, … are combinatorial variants).

### 2.7 Bound packages as `stdlib/<path>`

```text
stdlib/
  registry.go          Provider: import path → lazily built *runtime.Package
  bind/                helpers (fn, fnvc, fnvcRaw, hostType, …) — today's hostHelpers
  strings/  bytes/  strconv/  fmt/  time/  os/  encoding/json/  text/template/ …
```

- Each subpackage exports `func Install(r *bind.Registry)`; a package is
  *built* only when a script first imports it (today every engine pays the
  whole `installStdlib` up front — a quiet violation of I1's spirit).
- Pure-signature bindings (most of `strings`, `strconv`, `math`, `unicode`)
  are produced by `minigo gen-intrinsics` and checked in with a
  `// Code generated` header; hand-written files keep only adapters
  (callbacks, channels, timers, `FuncMap`, `sync.*` boxes).
- Each bound package gets a parity test against its source-interpreted
  form where `--src` works (the difffuzz verdict contract), which is the
  guard that prevents silent wrong answers (I5) when bindings drift.
- `WithAllowedRoots`/`WithHostPolicy` become filters on the registry
  instead of checks inside individual bindings.

### 2.8 Special forms as `special/`

`runtime.SymbolID`, `QuotedCall`, `SpecialFunc`, `SpecialContext` and
`vm.specialCtx` move to `special/`. `SpecialContext.ResolveType` returns a
`types.Ref`/`*types.Type`, giving convert-define the plan's §12.5 surface for
real. Registration remains `Engine.RegisterSpecial` (I7). The compiler
consults only the registry's `Has(SymbolID)` (I4: the stub package is never
loaded).

### 2.9 Root package becomes a facade

`minigo.go` keeps `Engine`, options and the public API. Moves:

| Today | Destination |
|---|---|
| `builtins.go` | `builtin/` |
| `dispatch.go` (method sets, embedding, iface) | `types/` |
| `intrinsics.go`, `hostvars.go`, `hostslice.go`, `asmimpl.go` | `stdlib/…` |
| `iterseq.go` | `hostffi/iterseq.go` |
| `inspect.go` | `inspect/` (bind side) |
| `embed.go`, `parse.go`, `parsecache.go` | `loader/` |
| `SourceOf`/`LoadFile`/`LoadDirScope` internals | `loader/` |
| `vet.go` | `cmd/minigo/vet` or `vet/` |

Type aliases in the root keep I7 (`type Value = runtime.Value` etc. already
pattern-match how examples import things).

## 3. Package-level dependency target

```text
syntax, resolve, pkg/locator           (no internal deps beyond each other)
index        → syntax
runtime      → syntax, bytecode(Chunk only)        [data]
loader       → resolve, syntax, index, runtime
constant     → runtime
types        → runtime, constant, loader(Indexed-only API)
bytecode     → syntax
compile      → bytecode, types(Oracle), runtime, special(registry Has only)
vm/sched     → runtime
vm           → bytecode, runtime, types, constant, vm/sched, hostffi(interface)
hostffi      → runtime, types, constant
builtin      → runtime (capability interfaces)
special      → runtime, types
stdlib/...   → stdlib/bind → runtime (capability interfaces), hostffi
minigo       → everything above
inspect, repl, storybook, cmd → minigo
```

`loader` exposes a narrow `Indexed(path) (*Package, error)` view to `types`
so the type layer *cannot* trigger `Initialize` (C1 enforced by the type
system rather than by review).

## 4. Migration plan

Each phase is behavior-preserving, lands as a series of small PRs, and must
pass `make format lint test`, `make test-race`, the pinned difffuzz cases and
the examples. Phases 1–2 are mechanical; 3–4 carry the design changes.

### Phase 0 — guardrails (small, do first)

- `archtest_test.go`: parse each package's imports with
  `parser.ParseFile(…, parser.ImportsOnly)` (no `go list`) and assert the
  §3 table. Start with the current graph as the allowed set and tighten it
  every phase (ratchet).
- File-size ratchet: a test failing when a non-generated, non-test `.go` file
  grows past its recorded size (budget only shrinks).
- Record baselines: `session_bench_test`, `genericbench_test`, realworld
  `grafana-openapi` wall time and alloc bytes. Every later phase reports
  deltas; regressions >10% need a justification in the PR.
- Add a `bytecode.Disasm` golden test set (a dozen representative functions)
  so compiler refactors show their effect as text diffs.

### Phase 1 — split files, no package moves (mechanical)

- `vm/vm.go` → `loop.go`, `frame.go`, `call.go`, `panic.go`, `trace.go`,
  `sched.go`, `ffi_marshal.go`, `ffi_iface.go`, `const.go`, `binop.go`,
  `convert.go`, `assert.go`, `generic.go`, `special.go`, `select.go`,
  `composite.go`, `iter.go` — following the existing `// ----` section
  comments.
- `compile/compile.go` → `scope.go`, `stmt.go`, `assign.go`, `expr.go`,
  `call.go`, `lit.go`, `constfold.go`, `typehint.go`, `goto.go`,
  `special.go`.
- `intrinsics.go` → one file per bound import path; `installStdlib` becomes a
  list of `installStrings(h)`, `installTime(h)`, … calls.
- Review with `git diff --color-moved=dimmed-zebra`; zero logic changes.
- Fold in the gosimilarity R1–R6 consolidations already validated in a
  scratch worktree (net −452 lines), including `(*TypeDef).TypeExpr()`.

### Phase 2 — extract packages along existing seams

Order chosen so each step has a narrow interface:

1. Capability interfaces (§2.2) with `VMCaller` as their union.
2. `vm/sched` (goroutines, park/wake, deadlock) — interacts with the loop
   only through `spawn`/`park`/`unpark`.
3. `special/` (§2.8).
4. `constant/` (UConst arithmetic and defaulting).
5. `hostffi/` (§2.6), then generate the `scriptIface` adapter family.
6. `builtin/`, then `stdlib/` with lazy per-package install (§2.7).
7. `loader/` (lifecycle state machine out of `runtime/package.go` and
   `minigo.go`).

After this phase the root package and `vm.go` are each under ~3,000 lines,
and `archtest` forbids `reflect` in `vm/` (outside `hostffi` adapters it is
constructed with).

### Phase 3 — the type model

1. Introduce `types.Type` with interning, built from `TypeDef` via
   `TypeDef.Type()`. Start with read-only consumers: method sets, interface
   satisfaction, `MethodSetOf` (single walk).
2. Move identity checks to `==` on `*types.Type`, deleting one twin/shape
   heuristic per PR, each backed by its pinned regression cases.
3. Host types: one `*types.Type` per `reflect.Type`; bound named types and
   their source declarations unify.
4. Generics inference/instantiation moves to `types/infer.go`; instance
   cache keys become `(*Function, []*types.Type)` instead of spelled
   strings (fixes "per-call key spelling" TODO).
5. Introduce `types.Oracle` and route the 14 `Materialize()` call sites in
   `compile/`/`vm/` through it (C1–C3); add `WithStrictLazyCompile` and run
   the conformance suite under it in CI.
6. Replace `staticOpTyp`/`opTyp`/`OpLocalTyp`/`OpGlobalTyp`/`statics` with
   oracle-backed hints stored in `CallSite`/slot metadata.

### Phase 4 — VM shape

In this order (each makes the next simpler): bytecode verifier + `MaxStack`
→ slot classification and direct store ops → contiguous per-goroutine stack
→ `CallSite` descriptors → inline caches for `OpGlobal`/`OpSelect`.
Exit criterion: the perf TODOs under "frame allocation", "non-param locals
are allocated twice", "operand stack grows from empty", "`chunkCellsEscape`
is true for nearly every chunk" and "selector resolution re-searches by name"
are closed or obsolete — achieved as a *by-product* of simpler code (I6).

### Phase 5 — optional, decision-gated

- Lazy materialization at symbol granularity (plan-minigo-vm §10's
  go-scan `FindSymbolInPackage` path) for very large packages — only if
  realworld runs show parse/index dominating.
- Revisit the tagged `Value` representation only if, after Phase 4, a profile
  shows boxing as a single hot spot above ~15%. Default answer: no.

## 5. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Large moves conflict with ongoing feature/bug PRs | Phase 1 lands file-by-file in short windows; moves and edits never share a PR. |
| Type interning changes observable identity (`%T`, `reflect.Type.String`) | Rendering stays derived from the declaration (`TypeDef`), not from the interned `Type`; difffuzz `%T` probes pin it. |
| Oracle hints become semantic (I5) | C3 + `WithStrictLazyCompile` CI job: hints may only speed up or tighten checks gc also performs. |
| Lazy `stdlib` install changes init order / side effects | Bindings are pure construction; add a test that two engines and a `NewSession` see identical bound packages. |
| Concurrency of inline caches on shared chunks | Atomic pointer per site; `make test-race` and `make concurrency-stress` gate Phase 4. |
| `VMCaller` users outside the repo | Keep `VMCaller` as the union interface; document capability interfaces as preferred. |

## 6. Explicit non-goals

- Introducing a static type checker or rejecting ill-typed programs at
  compile time (contradicts I1/I2).
- AST→AST macros (I4).
- A JIT, register VM rewrite, or tagged-value rewrite (I6).
- Changing scripts' source conventions or the `minigo.dev/host` stub API (I3).
