# Plan: Re-architecture of minigo (post-v1 consolidation)

> **Status**: proposal, revised after the spike in
> [spike-rearchitecture.md](./spike-rearchitecture.md). It is written from
> the viewpoint of a code owner who may freely restructure packages and
> reshape the VM, while keeping the project's founding ideas intact.
> Snapshot: main `a3f4e24` (2026-11-17).
>
> Related: [plan-minigo-vm.md](./plan-minigo-vm.md) (the original design),
> [ja/todo-priority.md](./ja/todo-priority.md),
> [ja/experiment-gosimilarity-refactoring.md](./ja/experiment-gosimilarity-refactoring.md),
> [experiment-frame-path-perf.md](./experiment-frame-path-perf.md).
> Japanese version: [ja/plan-rearchitecture.md](./ja/plan-rearchitecture.md).

## Changes in this revision

The spike measured the claims of the first draft. Sections marked
*(revised)* changed as a result:

- **§2.4.** "An unknown compile-time answer is always safe" (old C3) was
  false. Some compile-time lookups decide semantics, not speed. The contract
  now separates *hints* from *facts* (C3′), and `WithStrictLazyCompile` now
  means "defer every fact".
- **§1.2.7, §2.5.1.** Slot escapes come mostly from field reads and call
  receivers, not from stores. `OpNewLocal`/`OpRenewVar` also allocate fresh
  cells. Both points are now part of slot classification.
- **§2.2.** `VMCaller` has hidden capabilities, reached by interface
  assertion and by downcasting to `*VM`. They are now listed.
- **§2.3.** Interning is keyed on the *represented* type (PkgPath, Name),
  not on `reflect.Type`, and is done per engine. Only the host-twin helpers
  collapse to `==`; the shape helpers stay.
- **§2.7.** Lazy install is justified by `NewSession` cost, not `NewEngine`
  cost. Four couplings must be handled first.
- **§4.** Phase 0 gains the cheapest measured win: memoizing `go env` in
  `pkg/locator`.

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
| `intrinsics.go` | 11,033 | `installStdlib` alone is ~3,000 lines (one function) binding 45 import paths and 1,134 members |
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
   *(revised)* Where twins come from: a bound package returns a host value
   whose type is declared in a package interpreted from source. Examples are
   `os` returning `*fs.PathError` while `io/fs` runs from source, and host
   `go/ast` nodes meeting source `go/ast`. Run-time synthesis makes it worse:
   `vm.hostTypedefOf` and `hostPkgElemTypedef` mint a fresh TypeDef per
   call, and `ast.Expr` got 163 copies in one suite run.
2. **Static type knowledge is split between the compiler and the VM.** The
   compiler computes partial static types ad hoc (`staticOpTyp`,
   `callOpTyp`, `stampVarTyps`, `opTyp`) and ships them to the VM as
   `OpLocalTyp`/`OpGlobalTyp` stamps and per-call `statics []*TypeDef`
   (50 uses in vm.go), which the VM uses for inference and untyped-constant
   defaulting. There is no named contract for *what* the compiler may know.
3. **The laziness contract at compile time is implicit** *(revised)*.
   - The compiler calls `ImportRef.Materialize()` at 8 sites in `compile/`.
     It also reaches the `runtime/typedef.go` ones through `DisplayName`. It
     does this to type operands such as `pkg.Name`.
   - Measured: compilation never initializes a package. 6.5% of package
     builds in the root suite (162 of 2,502) start from the compiler.
   - The unwritten part is worse than "compile may go up to *Indexed*". Some
     of these answers are *semantic*: constness of `pkg.C`, nilability of
     `pkg.T`, the static type that lowers `rc == NoBody` to an interface
     compare, and composite-literal key types.
   - When the compiler withholds those answers, the suite produces wrong
     output (4 cases), a spurious trap (1) and lost gc rejections (9).
     Nothing marks which answers are which.
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
   dragging it along. *(revised)*
   - Of the 360 functions that take one, 96 never use it and 184 only
     forward it to a helper.
   - Its real surface is wider than 21 methods. Callers type-assert it to
     anonymous interfaces for `HostClear`, `HostMapDelete`, `HostNilTwin`,
     `MaterializeConstErr`, `CallCatching` and `ElemZero`, and `vm.vmOf`
     downcasts it to `*VM` at 7 sites.
6. **The intrinsic table regrew into per-package bindings** — the very
   thing plan-minigo-vm §11 wanted to collapse. That was the right call in
   practice (experiment-oapi-codegen-perf: "running work natively was the
   only lever worth more than ~7%"), but the shape is a 3,000-line function
   with no per-package ownership, no per-package tests and no generated
   parts despite `minigo gen-intrinsics` existing. *(revised)*
   - It is rebuilt for every engine *and every session*, with no cache.
   - For a trivial run through `NewSession`, that is 60–70% of wall time and
     about 94% of allocations.
   - Through `NewEngine` it is only about 7% of wall time. Two `go env`
     subprocesses in `pkg/locator` dominate there.
7. **Frame/locals layout is pessimistic by construction** *(revised)*.
   - Every local is a heap `*Cell`.
   - `OpLocalRef` is emitted for store targets *and* for every selector read
     and call receiver on a variable (`selectorBaseIsVar`). Reads and
     receivers are about 70% of all `OpLocalRef`. As a result
     `chunkCellsEscape` is true for 44.5% of chunks; under precise rules it
     would be about 10%.
   - Even an escape-free chunk saves little: `OpNewLocal` allocates a fresh
     cell per `:=`/`var` and `OpRenewVar` one per loop iteration, so the
     cell-block reuse (#1024) only covers parameters.
   - Each frame grows its own operand stack.
   - This is a *design* issue (the compiler does not classify slots), and
     the perf TODOs are its symptoms.

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
                            hints and facts through types.Oracle)
                bytecode/  (ops, Chunk, CallSite descriptors, verifier, disasm)
                                        │
  semantics     types/     (Type: resolved + interned; Ref: lazy;
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

### 2.2 Capability interfaces instead of `VMCaller` *(revised)*

Split the methods into small interfaces, each implemented by the VM and
accepted by the narrowest consumer. The split must include today's hidden
capabilities; otherwise moving a binding out still drags a concrete `*VM`
along.

```go
package runtime

type Caller interface   { Call(fn Value, args []Value) (Value, error); CallCatching(fn Value, args []Value) (Value, error); Recover() Value }
type Spawner interface  { Spawn(fn Value, args []Value) *Task; Task() *Task }
type TypeOps interface  { TypeOf(Value) *TypeDef; Zero(*TypeDef) Value; ElemZero(*TypeDef) Value; Convert(*TypeDef, Value) (Value, error); Equal(*TypeDef, Value, Value) bool; Copy(Value) Value; NewMap(*TypeDef) *Map; MapKeyOperand(*TypeDef, Value) Value }
type TypeResolver interface { ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error); FieldTypes(*TypeDef) ([]*TypeDef, error); ElemOf(*TypeDef) (*TypeDef, error); ArrayLenOf(*TypeDef) (int64, bool) }
type Members interface  { Member(base Value, name string) (Value, bool); MethodSetOf(Value) (map[string]bool, bool) }
type HostOps interface  { HostClear(Value) bool; HostMapDelete(m, k Value) bool; HostNilTwin(*TypeDef) Value; MaterializeConstErr(Value) error } // today reached by type assertion
type Frames interface   { CallerPCs() []uintptr; CallerFrame(pc uintptr) (CallSite, bool); Package() *Package }

// VMCaller stays as the union for source compatibility during migration.
type VMCaller interface { Caller; Spawner; TypeOps; TypeResolver; Members; HostOps; Frames }
```

(The `HostOps` signatures above are placeholders; the real ones come from
the existing assertion sites.)

Usage measured in the spike: `Call` 24 functions, `TypeOf` 16, `Convert` 13,
`ElemZero` 12, `Zero` 10, `ElemOf` 6, everything else at 5 or fewer. Most
bindings need nothing or only `Caller`, so this is the precondition for
moving bindings out of the root package.

Migration order:

1. Type the forwarding helpers (`runtime.IfaceMember`, `hostErrOf`,
   `display`, `callOrPanic`, `goJSONSeen`, …). They decide the real needs
   of the 184 functions that only forward the caller.
2. Drop the parameter from the 96 functions that never use it.
3. Replace the 7 `vmOf` downcasts with capability methods.

### 2.3 One type model: `types.Type` + `types.Ref` *(revised)*

```go
package types

// Ref is a lazy, unresolved type expression in a lexical context.
// Indexing produces Refs; nothing is loaded to build one.
type Ref struct { Expr ast.Expr; Scope *Scope }   // Scope: file imports + local types + generic binds

// Type is a resolved type, interned per Universe (one per Engine).
type Type struct {
    Kind    Kind
    Named   *NamedInfo   // pkg path + name + type args (nil for unnamed)
    Elem    *Type; Key *Type; Len int64
    Fields  []Field      // resolved lazily, once (embedding path precomputed)
    Methods *MethodSet   // lazily built once; shared by assertion and dispatch
    Host    reflect.Type // the host carrier, if any (may be a box type)
    Def     *runtime.TypeDef // back-pointer during migration
}

type Oracle interface {
    // Resolve may advance packages up to Indexed, never to Initializing.
    Resolve(Ref) (*Type, error)
}
```

- **Intern key.** A named type is interned by the type it *represents*,
  (PkgPath, Name, type args), not by its `reflect.Type`.
  - The carrier can be a box: `atomicBox` backs both `atomic.Value` and
    `atomic.Pointer`, and `timerChanBox`/`afterFuncTimer` stand for
    `*time.Timer`. Keying on `reflect.Type` would wrongly merge such types.
  - A bound named type (`time.Duration`) and its source declaration resolve
    to the same `*Type`.
- **Universe = Engine.** Bound tables are rebuilt per engine today (99 host
  types got a fresh TypeDef per engine). Process-wide identity would first
  need §2.7's sharing work, which is not planned.
- **One route for host typedefs.** Every host typedef goes through the
  intern table: `hostTypedefOf`, `hostPkgElemTypedef`, `HostTypeOf`, and
  generic-instantiation copies.
- **What collapses to `==`:** `hostTwinIs`, `hostNilTwin`,
  `sameSpelledTwin`, `HostNamedIs`/`HostTwinOf` and `sourceTwinOf`.
- **What stays:**
  - `tdShapeEq`, `underlyingShape`/`convShapeEq` and `structFieldsEq`
    implement Go's identical-underlying and assignability rules *between
    different types*. Interning does not remove them; hash-consing unnamed
    composite types would only make them cheaper.
  - `peelAlias` stays as long as aliases resolve lazily.
- **Values keep two representations.** A unified type can still be held as
  a `*runtime.Struct` or as a `GoValue`. The conversion layer
  (`hostErrTwins`, `goNative`, proxy boxes) stays and moves to `hostffi/`.
- `MethodSet` is computed once per type and serves `MethodSetOf`,
  `MethodFuncsOf`, interface satisfaction and promoted-member lookup (fixes
  the "walks the method set twice" TODO structurally).
- Selector resolution caches the *field index path* per `(Type, name)`
  (fixes "selector resolution re-searches by name").
- `runtime.TypeDef` survives as the declaration record (what index/compile
  produce); `TypeDef.Type()` lazily yields the interned `*Type`. Migration
  is call-site by call-site, never a flag day.

### 2.4 The compile-time laziness contract, written down *(revised)*

> **C1.** Compiling a function may advance a package referenced *syntactically
> in that function* up to `Indexed`. It must never run `Initialize`.
> (Measured: holds today, with zero violations across the suite and
> examples.)
>
> **C2.** All such advances go through a single `types.Oracle` owned by the
> compiler; no direct `ImportRef.Materialize()` in `compile/`.
>
> **C3′.** Every compile-time question is classified as one of two kinds:
>
> - A **hint** only makes code faster. Examples: a static argument typedef
>   that skips a dynamic lookup, or an inline-cache seed. If the oracle
>   cannot answer, the hint is dropped and the VM decides dynamically.
> - A **fact** decides semantics. Examples: is `pkg.C` a constant (shift
>   count constness)? Can `pkg.T` be nil? What is the static type of an
>   operand that decides how `==` lowers? What is a composite-literal key's
>   type? A fact is never dropped. The compiler either answers it, which C1
>   allows, or emits a *deferred fact*: an operand carrying a `types.Ref`
>   in the `CallSite`, which the VM resolves once at first execution.
>
> Facts also include gc's static rejections (`mismatched types
> time.Duration and int64`). A deferred one becomes a `TRAP` at the site,
> consistent with I2.

Why this replaced the first draft's C3 ("unknown is always safe"): with an
oracle that never answers, the suite gained 4 wrong outputs, 1 spurious trap
and 9 lost gc rejections. With an oracle that answers only for packages
already `Indexed`, 13 tests still failed, and which ones depends on load
order. See [spike-rearchitecture.md](./spike-rearchitecture.md) S2.

`compile/typehint.go` then replaces `staticOpTyp`/`callOpTyp`/`opTyp`/
`stampVarTyps`. Its output is per-instruction static information stored in
the chunk (see 2.5): hints, and facts or deferred facts. It replaces the side
channel of `OpLocalTyp` pushes and per-call `statics` slices.

`WithStrictLazyCompile()` makes the oracle defer *every* fact and drop every
hint. Running the conformance suite under it must give identical results,
only slower. That is a meaningful equivalence check: it proves each fact has
a run-time path. The first draft's version ("refuse what is not `Indexed`")
was not, because its results depended on load order.

### 2.5 VM shape

Changes, each independently landable and measured:

1. **Slot classification at compile time** *(revised)*.
   - Each local is either:
     - `Plain`: the value lives in the frame's slot array.
     - `Boxed`: a `*Cell`. A local is Boxed when a non-parent-upval closure
       captures it, when `&x`/`&x.f`/`&x[i]` takes its address, when a
       pointer-receiver method binds it, or when it is visible at a
       special-form call site (`trySpecial` snapshots every visible local
       into `QuotedCall.Locals`, and `specialCtx.Eval` shares the caller's
       cells).
   - Three kinds of operation must change, not only stores:
     - Store targets get direct ops (`OpSetLocal`, `OpSetLocalIndex`,
       `OpSetLocalField`) instead of `OpLocalRef` + generic ref stores.
     - Selector reads on a Plain local become `OpLocal` + `OpSelect`.
     - A call receiver `x.M()` needs the receiver kind of `M`. That is a
       *fact* in the §2.4 sense: answered statically, or Boxed
       conservatively when unknown.
   - `OpNewLocal` and `OpRenewVar` write into the slot in place for Plain
     locals. Without this, an escape-free chunk still allocates a cell per
     `:=` and per loop iteration.
   - Expected effect (spike S3/S4): escaping chunks drop from about 45% to
     about 10%, and about 5% of locals stay Boxed.
   - This removes the double allocation of non-param locals, makes
     `chunkCellsEscape` precise, and *simplifies* `assignRef`.
   - Code that reads frame cells generically and must be ported:
     - `selectMember`/`structMember` `case *Cell`
     - `assignRef`
     - `specialCtx.Resolve`
     - `localTypedefOf`
     - named results
     - range iterator slots

     None of these is a blocker.
2. **One contiguous value stack per goroutine.** Frames are windows
   `(base, sp)` into it; `Chunk.MaxStack` comes from the bytecode verifier.
   Arguments are passed in place (no `popArgs` slices, no receiver-prepend
   copy for bound methods).
3. **`CallSite` descriptors in the chunk.** One immutable `bytecode.CallSite`,
   referenced by `OpCall`'s operand, holds:
   - the static argument typedefs (hints)
   - the spread element typedef
   - the special-form quote index
   - deferred facts (§2.4)
   - the source position

   `frame.argbuf/statbuf` and `deferredCall.statics/spreadTd` disappear.
4. **Inline caches in `CallSite`/`Site` slots** (atomic pointer, so shared
   chunks stay race-free): `OpGlobal` caches the resolved cell guarded by an
   `Env` epoch that `:pin`/rebinding bumps; `OpSelect` caches
   `(*Type → access path)`. This is the "later `OpSelect` inline cache" of
   plan-minigo-vm §16, now possible because types are interned. A resolved
   deferred fact uses the same slot mechanism.
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
   v1 is over. (It uses no caller locals, so slot classification does not
   affect it.)

### 2.6 Host boundary as a package: `hostffi/`

Moves out of `vm.go` (roughly lines 3,950–8,600 and 12,000–14,400 today):
`goValueOf`, `toReflectValueDepth`, `hostMarshal`, `adaptFunc`,
`hostArgAs`, opaque stores, host nil/twin helpers, the `scriptIface`
family (`io.Reader`, `io.Writer`, `io.Seeker`, `fs.FS`, `fs.File`,
`fs.FileInfo`, `fs.DirEntry`, `sort.Interface`, `fmt.Stringer`, …), and the
host↔script value conversion layer that §2.3 keeps (`hostErrTwins`,
`goNative`, proxy boxes).

The adapter family is generated from a table (`interface → methods → arity`)
rather than hand-written per combination (`scriptFileSeek`,
`scriptFileSeekWT`, `scriptDirFileSeekWT`, … are combinatorial variants).

### 2.7 Bound packages as `stdlib/<path>` *(revised)*

```text
stdlib/
  registry.go          names: import path → member names (cheap, eager)
                       tables: import path → lazily built *runtime.Package
  bind/                helpers (fn, fnvc, fnvcRaw, hostType, …) — today's hostHelpers
  strings/  bytes/  strconv/  fmt/  time/  os/  encoding/json/  text/template/ …
```

- Each subpackage exports `func Install(r *bind.Registry, env bind.Env)`.
  A package's table is *built* only when it is first needed. Static
  cross-package couplings inside `installStdlib` are rare, so the split is
  mostly mechanical.
- **Justification.** The win is for sessions: `NewSession` re-runs the whole
  install, and that is 60–70% of a trivial session run's wall time and about
  94% of its allocations. For `NewEngine`, the `go env` subprocesses in
  `pkg/locator` cost more and are cheaper to fix (§4 phase 0).
- **Couplings to handle before going lazy:**
  1. *Install on host-type lookup.* `hostTwin` finds bound typedefs through
     `e.pkgs[rt.PkgPath()]` for host values flowing out of *other* packages
     (a `time.Time` from `ctx.Deadline()`). That lookup must trigger
     install, or it silently returns nil (an I5 hazard).
  2. *A name registry separate from the tables.* `BoundPackages`,
     `BoundSymbols`, `BoundOf`, `vet`, `SourceOf`, `NewSession`'s bind
     precedence and `loadPathMode`'s bound-shadow check all read the bound
     set eagerly. They move to the cheap `names` view.
  3. *Engine state is an explicit `bind.Env`.* About 45 closures capture the
     engine: `fsPath`, `cwd` (written by `os.Chdir`), `args`, output, and
     `AllowedRoots` deciding the `os`/`host` tables. Passing them in
     explicitly keeps tables per engine. Sharing tables across engines or
     sessions would need this state to move into the call context; that is
     not a goal of this plan.
  4. *No global mutable typedefs.* `templateFuncMap` is one package-level
     TypeDef shared by every engine, and `Bind` sets its `Pkg` only once.
     It becomes per-registry.
- **Generation.**
  - About half of the 869 entries are shapes `minigo gen-intrinsics` can emit
    today: one-statement wrappers, `GoValue` values and constants. They are
    checked in with a `// Code generated` header.
  - Typedefs with method lists or `HostScalar` (about 12%) need the
    generator extended first.
  - About 25% are real adapters and stay hand-written: callbacks, channels,
    timers, `FuncMap`, `sync.*` boxes, generics.
  - Generated wrappers go through `WrapFunc`, which reflects per call. That
    is slower than today's direct wrappers. It is accepted under I6, and a
    hot package can keep a hand-written wrapper.
- Each bound package gets a parity test against its source-interpreted
  form where `--src` works (the difffuzz verdict contract), which is the
  guard that prevents silent wrong answers (I5) when bindings drift.
- `WithAllowedRoots`/`WithHostPolicy` become filters on the registry
  instead of checks inside individual bindings (`hostPolicy` already filters
  generically in `Bind`).

### 2.8 Special forms as `special/`

`runtime.SymbolID`, `QuotedCall`, `SpecialFunc`, `SpecialContext` and
`vm.specialCtx` move to `special/`. `SpecialContext.ResolveType` returns a
`types.Ref`/`*types.Type`, giving convert-define the plan's §12.5 surface for
real. Registration remains `Engine.RegisterSpecial` (I7). The compiler
consults only the registry's `Has(SymbolID)` (I4: the stub package is never
loaded), and uses the same check to mark visible locals Boxed (§2.5.1).

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

Today `vm` imports `minireflect`, and `runtime` already imports `bytecode`;
both fit the target once `minireflect` moves under `hostffi`.

## 4. Migration plan

Each phase is behavior-preserving, lands as a series of small PRs, and must
pass `make format lint test`, `make test-race`, the pinned difffuzz cases and
the examples. Phases 1–2 are mechanical; 3–4 carry the design changes.

### Phase 0 — guardrails (small, do first)

- *(revised)* Memoize `go env GOROOT`/`GOMODCACHE` in `pkg/locator` per
  process. This is independent of everything else and is the largest single
  cost of `NewEngine`.
- `archtest_test.go`: parse each package's imports with
  `parser.ParseFile(…, parser.ImportsOnly)` (no `go list`) and assert the
  §3 table. Start with the current graph as the allowed set and tighten it
  every phase (ratchet).
- File-size ratchet: a test failing when a non-generated, non-test `.go` file
  grows past its recorded size (budget only shrinks).
- Record baselines: `session_bench_test`, `genericbench_test`, realworld
  `grafana-openapi` wall time and alloc bytes, and the spike's escape and
  compile-load counters. Every later phase reports deltas; regressions >10%
  need a justification in the PR.
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

1. Capability interfaces (§2.2), with `VMCaller` as their union. Start with
   the forwarding helpers, then fold in the hidden assertions, then the
   `vmOf` downcasts.
2. `vm/sched` (goroutines, park/wake, deadlock) — interacts with the loop
   only through `spawn`/`park`/`unpark`.
3. `special/` (§2.8).
4. `constant/` (UConst arithmetic and defaulting).
5. `hostffi/` (§2.6), then generate the `scriptIface` adapter family.
6. `builtin/`, then `stdlib/` (§2.7), in three steps:
   1. The name registry and `bind.Env`, with tables still built eagerly.
   2. `hostTwin`-triggered install.
   3. Lazy tables.
7. `loader/` (lifecycle state machine out of `runtime/package.go` and
   `minigo.go`).

After this phase the root package and `vm.go` are each under ~3,000 lines,
and `archtest` forbids `reflect` in `vm/` (outside `hostffi` adapters it is
constructed with).

### Phase 3 — the type model

1. Introduce `types.Type` with per-engine interning, built from `TypeDef`
   via `TypeDef.Type()`. Start with read-only consumers: method sets,
   interface satisfaction, `MethodSetOf` (single walk).
2. *(revised)* Route every host typedef producer through the intern table
   (`hostTypedefOf`, `hostPkgElemTypedef`, `HostTypeOf`, instantiation
   copies), keyed by the represented (PkgPath, Name). Make proxy boxes map to
   the type they represent.
3. Move identity checks to `==` on `*types.Type`, deleting the five
   host-twin helpers one per PR, each backed by its pinned regression cases.
   The shape helpers (`tdShapeEq`, `underlyingShape`/`convShapeEq`,
   `structFieldsEq`) and `peelAlias` stay. They move to `types/` as Go's
   identity and assignability rules.
4. Generics inference/instantiation moves to `types/infer.go`; instance
   cache keys become `(*Function, []*types.Type)` instead of spelled
   strings (fixes "per-call key spelling" TODO).
5. *(revised)* Introduce `types.Oracle`. First classify each of the 8
   `compile/` call sites, and the `runtime/typedef.go` ones the compiler
   reaches, as *hint* or *fact*. Then route them through the oracle (C1,
   C2), and add a deferred-fact path for every fact (C3′). Only then add
   `WithStrictLazyCompile` (defer all facts) and run the conformance suite
   under it in CI. The 13–15 tests that fail today under a non-answering
   oracle are the acceptance set: `gate_*`, `shift_const_count`,
   `litkey_named_peel`, `assign_pkgname_differs`, `src_pkgtype_maplit_keys`,
   `selnilable_trap`, `selconv_typearg` and `TestNetHTTPRoundtrip`.
6. Replace `staticOpTyp`/`opTyp`/`OpLocalTyp`/`OpGlobalTyp`/`statics` with
   oracle-backed hints and facts stored in `CallSite`/slot metadata.

### Phase 4 — VM shape

In this order (each makes the next simpler):

1. Bytecode verifier + `MaxStack`.
2. Slot classification, covering stores, selector reads and receivers, with
   in-place `OpNewLocal`/`OpRenewVar`.
3. Contiguous per-goroutine stack.
4. `CallSite` descriptors.
5. Inline caches for `OpGlobal`/`OpSelect`.

Exit criterion: these perf TODOs are closed or obsolete, achieved as a
*by-product* of simpler code (I6):

- "frame allocation"
- "non-param locals are allocated twice"
- "operand stack grows from empty"
- "`chunkCellsEscape` is true for nearly every chunk" (target: about 10% of
  chunks escaping)
- "selector resolution re-searches by name"

### Phase 5 — optional, decision-gated

- Lazy materialization at symbol granularity (plan-minigo-vm §10's
  go-scan `FindSymbolInPackage` path) for very large packages — only if
  realworld runs show parse/index dominating.
- Sharing bound tables across engines and sessions, by moving the
  engine-captured state into the call context. Only if session start-up is
  still significant after lazy install.
- Revisit the tagged `Value` representation only if, after Phase 4, a profile
  shows boxing as a single hot spot above ~15%. Default answer: no.

## 5. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Large moves conflict with ongoing feature/bug PRs | Phase 1 lands file-by-file in short windows; moves and edits never share a PR. |
| Type interning changes observable identity (`%T`, `reflect.Type.String`) | Rendering stays derived from the declaration (`TypeDef`), not from the interned `Type`; difffuzz `%T` probes pin it. |
| Interning merges distinct types that share a host carrier (`atomicBox`) | Key by the represented (PkgPath, Name), never by `reflect.Type`. Add a pinned case for `atomic.Value` vs `atomic.Pointer`. |
| Compile-time answers turn out to be semantic (I5) | C3′: every question is a hint or a fact; facts are answered or deferred, never dropped. The `WithStrictLazyCompile` CI job checks that every fact has a run-time path. |
| A deferred fact changes *when* an error appears (gc rejects at compile time; minigo traps on execution) | Allowed by I2. Pin the message text with the existing `gate_*` cases. |
| Lazy `stdlib` install misses a bound typedef for a host value from another package | `hostTwin` lookups trigger install (§2.7 coupling 1); a test runs a `ctx.Deadline()` → `time.Time` flow with `time` never imported. |
| Lazy `stdlib` install changes init order / side effects | Bindings are pure construction; add a test that two engines and a `NewSession` see identical bound packages. |
| Concurrency of inline caches on shared chunks | Atomic pointer per site; `make test-race` and `make concurrency-stress` gate Phase 4. |
| `VMCaller` users outside the repo | Keep `VMCaller` as the union interface; document capability interfaces as preferred. |

## 6. Explicit non-goals

- Introducing a static type checker or rejecting ill-typed programs at
  compile time (contradicts I1/I2). Deferred facts trap on execution; they do
  not reject the program.
- AST→AST macros (I4).
- A JIT, register VM rewrite, or tagged-value rewrite (I6).
- Changing scripts' source conventions or the `minigo.dev/host` stub API (I3).
- Process-wide type identity or shared bound tables across engines (see
  Phase 5).
