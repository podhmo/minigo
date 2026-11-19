# Plan: Re-architecture of minigo (post-v1 consolidation)

> **Status**: proposal, third revision. It merges three sources:
>
> - **D-own**: the ownership and lifetime design
>   ([discussion #1090, comment 19667469](https://github.com/podhmo/minigo/discussions/1090#discussioncomment-19667469)).
>   This plan adopts its vocabulary, layout and milestones.
> - **D-vm**: this plan's own first draft: VM shape, capability interfaces,
>   per-package stdlib.
> - **The spike**: [spike-rearchitecture.md](./spike-rearchitecture.md),
>   which measured D-vm's assumptions.
>
> Written from the viewpoint of a code owner who may freely restructure
> packages and reshape the VM, while keeping the project's founding ideas
> intact. Snapshot: main `a3f4e24` (2026-11-17).
>
> Related: [plan-minigo-vm.md](./plan-minigo-vm.md) (the original design),
> [ja/todo-priority.md](./ja/todo-priority.md),
> [ja/experiment-gosimilarity-refactoring.md](./ja/experiment-gosimilarity-refactoring.md),
> [experiment-frame-path-perf.md](./experiment-frame-path-perf.md).
> Japanese version: [ja/plan-rearchitecture.md](./ja/plan-rearchitecture.md).
> Actionable task list (Japanese): [../next/ja/TODO.md](../next/ja/TODO.md).

## How the three sources fit together

The two designs answer different questions, so they mostly compose.

- **D-own** decides *who owns what and for how long*:
  - shared, immutable source definitions against per-session execution state;
  - a semantics service that answers declaration and type questions
    without executing anything;
  - explicit result kinds for "not known yet".
- **D-vm** decides *how execution works inside that frame*:
  - the shape of frames and slots;
  - `CallSite` descriptors;
  - the capability interfaces that replace `VMCaller`;
  - per-package lazy bindings.

Where they conflict, §8 compares the two on their merits and records why
one was chosen; it does not default to either source. The table below only
summarizes the outcome:

| Topic | D-vm (draft) | D-own | Spike evidence | Chosen (see §8) |
|---|---|---|---|---|
| Compile-time lookups | Unknown is always safe (C3) | Results are Found / Missing / **Deferred** / Failed; Deferred is not invalid Go | Unknown is *not* always safe: 4 wrong outputs, 1 spurious trap, 9 lost gc rejections (S2) | D-own's result kinds, plus the spike's hint/fact split (§2.4) |
| Strict mode | Answer only for packages already `Indexed` | Policy `IndexedOnly` | `IndexedOnly` alone breaks 13 tests, depending on load order | `IndexedOnly` is valid only when every fact has a Deferred path (§2.4) |
| Type identity | One interned `*Type` per `reflect.Type`; pointer `==` | `TypeID` from declaring source, position, type args and source version; TypeDef pointers may differ per Session | Keying on `reflect.Type` is wrong (`atomicBox`); 5 of 9 helpers collapse | `semantics.TypeID` as the key; runtime interning per Session keyed by `TypeID` (§2.3) |
| Semantics dependencies | `types` imports `runtime` (with a `Def` back-pointer) | `semantics` must not depend on runtime, VM or compiler | — | D-own; adapters live on the execution side (§2.3) |
| Layout | `types/`, `loader/`, `hostffi/`, `stdlib/`, `vm/sched/` | `internal/{source, semantics, execution, process, hostbridge, stdlib, archcheck}` | — | D-own's names (§2.1) |
| Bound tables | Lazily built, shared | Bindings inherited at `NewSession`; special forms shared live; state that differs per Session needs a factory | Bound tables capture engine state (S6) | A lazy per-registry build with an explicit `bind.Env` as the factory input (§2.7) |
| Order | Phases 0–5 | M0–M6 | — | One merged sequence (§4) |

## 0. What must not change

| # | Invariant | Source |
|---|---|---|
| I1 | **Lazy Go** — a package is located/parsed/indexed/initialized only when execution reaches it. No `go/types`, no `go/packages`, no `go list`. | plan-minigo-vm §1, §4, §5; AGENTS.md |
| I2 | **Total compiler** — the compiler never rejects an AST; unsupported constructs become `TRAP` that fires only if executed. | plan-minigo-vm §3 |
| I3 | **Scripts are valid Go** — gopls/vet work on scripts; interpreter extensions are stub packages (`minigo.dev/host`, `define`). | plan-minigo-vm §1, §11 |
| I4 | **Quoted Go** — special forms dispatch by canonical symbol identity at run time, never AST→AST macros, and never load the stub package. | plan-minigo-vm §12 |
| I5 | **Never pretend success** — a silent wrong answer is worse than a trap. | ja/todo-priority.md |
| I6 | **Speed is a non-goal** — simplicity and coverage beat run-time speed; perf work is justified only when it also simplifies or removes a measured pathology. | README |
| I7 | **Public embedding API stays source-compatible** — `NewEngine`, `Run`, `Call`, `Bind`, `RegisterSpecial`, `WrapFunc`, `ValueOf`, `NewSession`, `inspect`, `repl`, `storybook` users (all `examples/*`) keep compiling, or get a mechanical migration. | examples/ |
| I8 | **Shared definitions, owned state** (D-own) — what comes from source is shared; what execution creates is owned by one Session. A declaration-only question never initializes or executes. | D-own §4–§6 |

Everything else — package boundaries, the VM's frame/stack layout, the type
model, the intrinsic table's shape — is open.

### 0.1 Vocabulary

This plan uses D-own's glossary. The terms that matter most here:

- **Index**: the per-package declaration table.
- **Resolve**: find a declaration in context. Implies no execution.
- **Materialize**: build a runtime type, function or value. It always names
  its target. The existing `ImportRef.Materialize` actually *indexes* a
  package; an adapter makes that explicit.
- **Initialize**: package vars and `init`.
- **TypeRef**: a lazy type expression with its context.
- **TypeID**: type identity, independent of runtime object addresses.
- **PackageImage**: an immutable definition for one source version.
- **PackageInstance**: a PackageImage's execution state within one
  Session.
- **Engine / Session / Process / VM / Frame / Chunk / Storage**: the
  ownership units, as defined in D-own.
- **Panic** is recoverable by a script; **Trap** is not.

Bare "load" is not used: say which object and which stage.

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

### 1.2 Structural problems (in order of cost)

1. **Definition and execution state are not separated** (D-own).
   - `runtime.Package` holds both a source definition (AST, index, import
     specs) and execution state (globals, init state, `ImportRef`s whose
     `sync.Once` caches a loaded package).
   - So nothing can be shared safely between Sessions, and nothing states
     who owns a cache.
2. **There is no single type model.**
   - `runtime.TypeDef` is both a lazy AST reference and a resolved type.
     Identity is approximated by heuristics spread over the VM:
     `tdShapeEq`, `sameSpelledTwin`, `hostNilTwin`, `hostTwinIs`,
     `peelAlias`, `structFieldsEq`, `underlyingShape`, …
   - Spike S7 shows these heuristics answer *different* questions:
     - five resolve host/source twins;
     - four implement Go's identical-underlying and assignability rules.

     D-own's split into four questions (identity, assignability,
     convertibility, interface satisfaction) is the right shape.
   - Twins arise when a bound package returns a host value whose type is
     declared in a source-interpreted package. Run-time synthesis
     (`hostTypedefOf`, `hostPkgElemTypedef`) mints a fresh TypeDef per call:
     `ast.Expr` got 163 copies in one suite run.
3. **Static type knowledge is split between the compiler and the VM**:
   - `staticOpTyp`, `callOpTyp`, `stampVarTyps` and `opTyp` on the compiler
     side;
   - `OpLocalTyp`/`OpGlobalTyp` stamps and per-call `statics` on the VM side.

   There is no named contract for what the compiler may know.
4. **The compile-time laziness contract is implicit, and "unknown" is
   encoded as nil.**
   - The compiler calls `ImportRef.Materialize()` at 8 sites, plus the
     `runtime/typedef.go` ones it reaches through `DisplayName`.
   - Measured: compilation never initializes a package (0 violations), and
     6.5% of package builds start from the compiler.
   - Some answers are *semantic*: constness, nilability, the operand type
     that decides `==` lowering, and literal key types. Withholding them
     breaks 15 tests (spike S2).
   - Today nil/false mean "absent", "failed" and "not known yet" at once,
     which D-own forbids.
5. **The VM is a god object.** `*VM` owns all of these:
   - the dispatch loop, frames, defer/panic/recover;
   - goroutine scheduling and deadlock detection;
   - the reflect boundary and the script→host adapters;
   - untyped-constant arithmetic;
   - interface satisfaction;
   - generics inference;
   - `specialCtx`.
6. **`VMCaller` is a 21-method interface with a hidden surface.**
   - Of the 360 functions that take one, 96 never use it and 184 only
     forward it.
   - Callers type-assert it for `HostClear`, `HostMapDelete`, `HostNilTwin`,
     `MaterializeConstErr`, `CallCatching` and `ElemZero`.
   - `vm.vmOf` downcasts it to `*VM` at 7 sites.
7. **The intrinsic table is a 3,000-line function, rebuilt per Engine and
   per Session.**
   - For a trivial run through `NewSession` it is 60–70% of wall time and
     about 94% of allocations.
   - Through `NewEngine`, two `go env` subprocesses in `pkg/locator`
     dominate instead.
8. **Frame/locals layout is pessimistic by construction.**
   - Every local is a heap `*Cell`.
   - `OpLocalRef` is emitted for store targets and for every selector read
     and call receiver on a variable (about 70% of all `OpLocalRef`). So
     `chunkCellsEscape` is true for 44.5% of chunks; under precise rules it
     would be about 10%.
   - `OpNewLocal`/`OpRenewVar` allocate a fresh cell per `:=` and per loop
     iteration.

## 2. Target architecture

### 2.1 Layout (D-own's tree, with D-vm's contents placed in it)

```text
minigo
├── minigo.go, session.go   public API, assembly, compatibility window
├── internal
│   ├── source        resolve + file selection + source versions + PackageImage
│   │                 (AST, index, import specs); shared, immutable after publish
│   ├── semantics     declaration and type queries: TypeRef, TypeID, the four
│   │                 relations, method sets, field paths, generic inference;
│   │                 no runtime, vm or compile imports
│   ├── execution     Session, PackageInstance, init state machine,
│   │                 materialization, the TypeID → runtime type intern table,
│   │                 the special-form registry and context implementation
│   ├── process       goroutines, park/wake, deadlock detection, callback lifetime
│   │                 (D-vm's vm/sched)
│   ├── hostbridge    reflect boundary, script→host adapters (generated family),
│   │                 host↔script value conversion (D-vm's hostffi)
│   ├── stdlib        bind registry + per-package Install (§2.7)
│   ├── archcheck     dependency and sharing lint (go/parser only)
│   └── cmd/archcheck
├── syntax  index  resolve  pkg/locator
├── compile           AST → bytecode; slot classification; hints and facts (§2.4)
├── bytecode          ops, Chunk, CallSite descriptors, verifier, disasm
├── vm                dispatch loop, frames, calls, defer/panic/recover
├── runtime           public values and base execution contracts (data only)
├── builtin           len, append, make, … (capability interfaces)
├── special           public: SymbolID, QuotedCall (value-free types only)
│                     (registry and the VM-side context implementation sit in internal/execution)
├── inspect  minireflect  repl   consumers of semantics, never their own engine
```

Responsibilities and exclusions follow D-own §11. D-vm adds:

- `vm` must not import `reflect`. Host values are opaque `*runtime.GoValue`,
  and every host interaction goes through `hostbridge`.
- `semantics` imports only `syntax`, `index` and `internal/source`.
  Runtime-side callers reach it through an adapter in `execution`.
- Untyped-constant arithmetic on runtime values stays on the execution side
  (`vm` or `execution`). `semantics` only answers "is this a constant, and
  of what declared type".

### 2.2 Capability interfaces instead of `VMCaller`

Split the methods, *including the hidden ones*, into small interfaces. The
VM implements all of them; each consumer takes the narrowest.

```go
package runtime

type Caller interface   { Call(fn Value, args []Value) (Value, error); CallCatching(fn Value, args []Value) (Value, error); Recover() Value }
type Spawner interface  { Spawn(fn Value, args []Value) *Task; Task() *Task }
type TypeOps interface  { TypeOf(Value) *TypeDef; Zero(*TypeDef) Value; ElemZero(*TypeDef) Value; Convert(*TypeDef, Value) (Value, error); Equal(*TypeDef, Value, Value) bool; Copy(Value) Value; NewMap(*TypeDef) *Map; MapKeyOperand(*TypeDef, Value) Value }
type TypeResolver interface { ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error); FieldTypes(*TypeDef) ([]*TypeDef, error); ElemOf(*TypeDef) (*TypeDef, error); ArrayLenOf(*TypeDef) (int64, bool) }
type Members interface  { Member(base Value, name string) (Value, bool); MethodSetOf(Value) (map[string]bool, bool) }
type HostOps interface  { HostClear(Value) bool; HostMapDelete(m, k Value) bool; HostNilTwin(*TypeDef) Value; MaterializeConstErr(Value) error } // placeholder signatures; today reached by type assertion
type Frames interface   { CallerPCs() []uintptr; CallerFrame(pc uintptr) (CallSite, bool); Package() *Package }

type VMCaller interface { Caller; Spawner; TypeOps; TypeResolver; Members; HostOps; Frames } // kept for I7
```

Measured use: `Call` 24 functions, `TypeOf` 16, `Convert` 13, `ElemZero`
12, `Zero` 10, `ElemOf` 6, the rest at 5 or fewer.

Migration order:

1. Type the forwarding helpers first.
2. Drop the parameter from the 96 functions that never use it.
3. Replace the 7 `vmOf` downcasts with capability methods.

Each capability call runs on behalf of a specific Session and Process.
Following D-own §9, a callback must declare which Session and Process it
returns to, and whether it may run after that Process ends.

### 2.3 Type model: `semantics.TypeID` + per-Session runtime interning

```go
package semantics

// TypeRef is a lazy type expression in its declaring context.
type TypeRef struct { Expr ast.Expr; Ctx DeclContext } // package image, file imports, type-arg binds

// TypeID is identity independent of runtime addresses.
//   named source type: (source version, declaring package, decl position, type args)
//   bound host type:   (PkgPath, Name, type args) of the *represented* type
//   unnamed type:      structural (kind + component TypeIDs)
type TypeID struct{ /* comparable */ }

type Result[T any] struct { Kind ResultKind; Val T; Err error } // Found | Missing | Deferred | Failed

type Service interface {
    TypeOf(ref TypeRef, p Policy) Result[TypeID]          // Policy: IndexedOnly | AllowIndex
    Identical(a, b TypeID) Result[bool]
    AssignableTo(v, t TypeID) Result[bool]
    ConvertibleTo(v, t TypeID) Result[bool]
    Implements(t, iface TypeID) Result[bool]
    MethodSet(t TypeID, p Policy) Result[*MethodSet]
    FieldPath(t TypeID, name string, p Policy) Result[[]int]
}
```

Key decisions:

- **Identity has a TypeID key.**
  - Bound named types are keyed by the type they *represent*, not by
    `reflect.Type`. `atomicBox` carries both `atomic.Value` and
    `atomic.Pointer`, and `timerChanBox`/`afterFuncTimer` stand for
    `*time.Timer`.
  - A bound `time.Duration` and a source-declared `time.Duration` from the
    same source version get the same TypeID.
  - Types from different source versions are never identified by path and
    name alone (D-own §7).
- **Runtime interning is per Session.**
  - `execution` holds `TypeID → *runtime.TypeDef`. Within a Session the
    host-twin helpers (`hostTwinIs`, `hostNilTwin`, `sameSpelledTwin`,
    `HostNamedIs`/`HostTwinOf`, `sourceTwinOf`) collapse to pointer
    equality.
  - Across Sessions, pointers differ, which D-own explicitly allows;
    comparison goes by TypeID.
  - Every host typedef producer goes through that table:
    `hostTypedefOf`, `hostPkgElemTypedef`, `HostTypeOf`, instantiation
    copies.
- **The four relations stay four.** `tdShapeEq`, `underlyingShape`/
  `convShapeEq` and `structFieldsEq` become `AssignableTo`/`ConvertibleTo`/
  `Identical` in `semantics`; they do not disappear. `peelAlias` becomes
  part of `TypeOf` resolution.
- **Values keep two representations.** A unified type can still be held as
  a `*runtime.Struct` or a `GoValue`. The conversion layer (`hostErrTwins`,
  `goNative`, proxy boxes) moves to `hostbridge`, with an explicit
  copy-or-share contract per value kind (D-own §9).
- `MethodSet` and per-`(TypeID, name)` field paths are computed once per
  image and are shareable. They fix the "walks the method set twice" and
  "selector resolution re-searches by name" TODOs.
- Generic inference splits in two. Today `inferCached` takes run-time
  argument *values* and `statics`.
  - Unification over TypeIDs is a pure rule, so it lives in `semantics`.
  - Mapping argument values to TypeIDs touches `runtime.Value`, so it stays
    in the execution adapter.
  - Instance caches key on `(function decl, []TypeID)` instead of spelled
    strings.
- Rendering (`%T`, `reflect.Type.String`) stays derived from the
  declaration, not from the TypeID.

### 2.4 The compile-time contract: policies, results, hints and facts

> **C1.** Compiling a function may use policy `AllowIndex` for packages
> referenced syntactically in that function. It must never initialize, and
> never run script or host code. (Measured: 0 violations today.)
>
> **C2.** The compiler asks only through the semantics service, via a
> compiler adapter; there is no direct `ImportRef.Materialize()` in
> `compile/`. Every query returns Found, Missing, Deferred or Failed; nil
> never encodes absence.
>
> **C3′.** Each compile-time question is either a hint or a fact:
>
> - A **hint** only speeds things up, for example a static argument typedef
>   or an inline-cache seed. Non-Found means drop it.
> - A **fact** decides semantics: constness (shift count), nilability of a
>   qualified type, the operand type that lowers `==`, a literal key type,
>   receiver kind for slot classification (§2.5), and gc's static
>   rejections.
>   - Found: use it.
>   - Missing: the declaration does not exist; compile a `TRAP` with gc's
>     message.
>   - **Deferred**: emit a *deferred fact*, a `TypeRef` in the `CallSite`
>     that the VM resolves at first execution under `AllowIndex`.
>   - Failed: compile a `TRAP` carrying the cause. Semantics never erases
>     the cause; trapping is the adapter's choice (D-own §6).

Under policy `IndexedOnly`, every fact about a package not yet indexed
comes back Deferred, so `IndexedOnly` is only correct once every fact has a
deferred path. The spike measured the gap: an `IndexedOnly`-like oracle
fails 13 tests today, and an oracle that never answers fails 15.

`WithStrictLazyCompile()` runs the compiler under `IndexedOnly` with every
fact forced to Deferred. The conformance suite must give identical results
under it. That proves each fact has a run-time path. It is the CI
equivalence check, and the 15 tests above are its acceptance set.

`compile/typehint.go` replaces `staticOpTyp`/`callOpTyp`/`opTyp`/
`stampVarTyps`. Its output is per-instruction information stored in the
chunk: hints, facts and deferred facts. It replaces the `OpLocalTyp` pushes
and the per-call `statics`.

### 2.5 VM shape

Each step lands independently and is measured.

1. **Slot classification.**
   - A local is `Plain` (value in the frame slot) or `Boxed` (`*Cell`).
   - A local is Boxed when:
     - a non-parent-upval closure captures it;
     - `&x`/`&x.f`/`&x[i]` takes its address;
     - a pointer-receiver method binds it. The receiver kind is a fact in
       the §2.4 sense; Deferred means Boxed.
     - it is visible at a special-form call site. `trySpecial` snapshots
       every visible local, and `specialCtx.Eval` shares the caller's
       cells.
   - Stores use direct ops (`OpSetLocal`, `OpSetLocalIndex`,
     `OpSetLocalField`), and selector reads on Plain locals use `OpLocal` +
     `OpSelect`.
   - `OpNewLocal`/`OpRenewVar` write in place for Plain locals.
   - Expected effect: escaping chunks go from about 45% to about 10%, and
     about 5% of locals stay Boxed.
   - Code to port (none is a blocker): `selectMember`/`structMember`,
     `assignRef`, `specialCtx.Resolve`, `localTypedefOf`, named results,
     range iterator slots.
   - D-own §9's copy/share table is the contract each op must keep. Struct
     and array values copy at Go copy points; slices share the backing
     array; maps share; pointers and Storage keep referent identity.
2. **One contiguous value stack per goroutine.** Frames are `(base, sp)`
   windows into it. `Chunk.MaxStack` comes from the verifier, and arguments
   are passed in place.
3. **`CallSite` descriptors.** One immutable record per call site holds:
   - the static argument hints;
   - the spread element type;
   - the quote index;
   - deferred facts;
   - the source position.

   `frame.argbuf/statbuf` and `deferredCall.statics/spreadTd` go away.
4. **Inline caches** in `CallSite`/`Site` slots, as atomic pointers.
   - `OpGlobal` caches the resolved cell, guarded by an `Env` epoch.
   - `OpSelect` caches `(TypeID → field path)`.
   - Resolved deferred facts use the same slots.
   - Caches holding Session-dependent references live with the Chunk, so a
     Chunk is not shareable across Sessions. Sharing compiled code stays
     optional (§4, M6).
5. **Bytecode verifier** in tests and under a debug option. It checks stack
   effects, jump targets and slot kinds, and computes `MaxStack`.
6. **Keep**: `Value = any`, `Tuple`, the Trap/Panic distinction,
   per-function lazy compilation, and `SPECIAL_CALL`. A tagged `Value` is
   out of scope (I6).
7. **Rename `OpEvalAST` to `OpEvalFragment`** and keep it only as the
   REPL/`EvalExpr` bridge.

### 2.6 Execution state and ownership (D-own, summarized)

- `PackageImage` lives in `internal/source`. It is immutable once
  published and shared by Sessions for the same source version.
  - It holds AST, index, declaration positions, import *specs*, and the
    language version and build settings.
  - It never holds init results, global cells, `Function` objects,
    materialized TypeDefs, or host-capturing closures.
- `PackageInstance` lives in `internal/execution`. It is per Session and
  holds globals, init state, runtime import instances, and materialized
  functions and types.
  - `ImportRef` values that cache a loaded package are instance state.
  - The existing `runtime.Package` stays as a compatibility window.
- Instance states are uninitialized, initializing, available and failed.
  - Re-entry from the same execution path is distinguished from waiting
    from another goroutine.
  - Existing behavior for partial-init reads and cycles is pinned by tests
    before it changes.
- Source versions cover contents, file set, language version and build
  settings.
  - A Session keeps the definitions it fetched.
  - A Session created after an edit fetches the new version.
  - Live Sessions are not patched in place.
- Host registrations: bindings are inherited at `NewSession`, and
  special-form registrations are shared live. This plan does not change
  either; snapshotting registrations is a separate compatibility decision.

### 2.7 Bound packages: `internal/stdlib`

```text
internal/stdlib/
  registry.go   names: import path → member names (cheap, eager)
                tables: import path → built on first need, per Session
  bind/         helpers (fn, fnvc, fnvcRaw, hostType, …) and bind.Env
  strings/ bytes/ strconv/ fmt/ time/ os/ encoding/json/ text/template/ …
```

- Each subpackage exports `Install(r *bind.Registry, env bind.Env)`.
  `bind.Env` is the per-Session factory input D-own §9 asks for: `fsPath`,
  `cwd`, `args`, output, and `AllowedRoots`.
- **Justification.** `NewSession` re-runs the whole install today, which is
  60–70% of a trivial session run. For `NewEngine`, the `go env`
  subprocesses cost more and are cheaper to fix (§4, M0).
- **Couplings to handle first** (spike S6):
  1. `hostTwin`'s `e.pkgs[rt.PkgPath()]` lookup must trigger install.
     Otherwise it silently returns nil, which breaks I5.
  2. `BoundPackages`, `BoundSymbols`, `BoundOf`, `vet`, `SourceOf`, the
     `NewSession` precedence and the bound-shadow check move to the cheap
     `names` view.
  3. Engine-captured state moves into `bind.Env`.
  4. The global `templateFuncMap` becomes per-registry.
- **Generation.**
  - About half of the 869 entries can be emitted by `gen-intrinsics` today,
    with a `// Code generated` header.
  - Typedefs with method lists need the generator extended (about 12%).
  - Real adapters stay hand-written (about 25%).
  - Generated wrappers reflect on every call. This is accepted under I6,
    and hot packages may keep direct wrappers.
- Each bound package gets a parity test against its source-interpreted
  form where `--src` works.
- `WithAllowedRoots`/`WithHostPolicy` become registry filters.

### 2.8 Special forms: `special/`

- `SymbolID` and `QuotedCall` live in the public `special/` package with
  aliases left in `runtime` (I7, see K8). `SpecialFunc`/`SpecialContext`
  stay in `runtime`: their signatures reference `runtime.Value`,
  `*Package` and `*TypeDef`, so moving them would create a
  runtime <-> special cycle. Examples consume the `runtime.*` surface and
  are unaffected. The registry moved to `internal/execution.Registry`;
  the VM-side context implementation (`vm.specialCtx`) stays in `vm`
  because it needs `*VM`/`*frame` and `internal/execution` cannot import
  runtime/vm.
- Dispatch uses canonical symbol identity from the declaring package. That
  means import aliases, local shadowing and method origin are all respected.
- The compiler asks the registry only `Has(SymbolID)`, and never loads the
  stub package (I4).
- The special-form context carries:
  - the syntax position and declaration context;
  - semantics access for type questions;
  - a separate, explicit evaluation capability bound to the calling
    Session and Process.

  `semantics` gets no `Eval`.
- `SpecialContext.ResolveType` returns a `semantics.TypeRef`/`TypeID`.

### 2.9 Root package becomes a facade

| Today | Destination |
|---|---|
| `builtins.go` | `builtin/` |
| `dispatch.go` (method sets, embedding, iface) | `internal/semantics` (rules) + `internal/execution` adapter |
| `intrinsics.go`, `hostvars.go`, `hostslice.go`, `asmimpl.go` | `internal/stdlib/…` |
| `iterseq.go` | `internal/hostbridge` |
| `inspect.go` | `inspect/` (a consumer of `semantics`) |
| `embed.go`, `parse.go`, `parsecache.go`, `SourceOf`/`LoadFile`/`LoadDirScope` internals | `internal/source` |
| lifecycle in `runtime/package.go`, `minigo.go` | `internal/execution` |
| `vet.go` | `cmd/minigo/vet` |

Type aliases in the root keep I7.

## 3. Dependency target

```text
syntax, resolve, pkg/locator           (no internal deps beyond each other)
index                → syntax
internal/source      → resolve, syntax, index, pkg/locator
internal/semantics   → internal/source, syntax, index          (no runtime/vm/compile)
bytecode             → syntax
runtime              → syntax, bytecode(Chunk only)             [data]
compile              → bytecode, runtime, internal/semantics (via adapter), special(Has only)
internal/process     → runtime
internal/hostbridge  → runtime, internal/semantics
vm                   → bytecode, runtime, internal/process, internal/hostbridge(interface)
internal/execution   → internal/source, internal/semantics, runtime, compile, vm, special
builtin              → runtime (capability interfaces)
special              → syntax (identifiers and quoted calls only, no values)
internal/stdlib/...  → internal/stdlib/bind → runtime (capabilities), internal/hostbridge
minigo               → everything above
inspect, minireflect, repl, storybook, cmd → minigo (inspect/minireflect ask internal/semantics)
```

`internal/source` exposes `IndexedOnly`/`AllowIndex` views to `semantics`,
so the type layer *cannot* initialize. C1 is then enforced by the import
graph.

## 4. Migration: one merged sequence

M-numbers are D-own's milestones; P-numbers are D-vm's phases. Every step
passes `make format lint test`, the pinned difffuzz cases and the examples.
Steps that touch shared state or callbacks also run `make test-race`.

| Step | Content | Completion |
|---|---|---|
| **M0** + P0 | Pin behavior: laziness, metadata fetch without execution, Session separation, update after edit, Quoted Go. Memoize `go env GOROOT`/`GOMODCACHE` in `pkg/locator`. Add `bytecode.Disasm` goldens. Record benchmarks and the spike's counters (compile-triggered builds, escape ratio). Pin the spike's 15-test acceptance set as "known to depend on compile-time facts". | Protected behaviors and known differences can be stated; baselines exist |
| **M1** | Mechanical split into `sharedAssets` and `sessionState`. `internal/cmd/archcheck` with a field allowlist that recurses into cache entries, with negative and positive examples. | Ownership is visible in the structure; no API or sharing change |
| **P1** | Split `vm/vm.go`, `compile/compile.go` and `intrinsics.go` into files along existing sections; fold in gosimilarity R1–R6. Zero logic change, reviewed with `--color-moved`. | Files under the size ratchet |
| **M2** | `internal/semantics` with type-declaration queries, policies `IndexedOnly`/`AllowIndex`, results Found/Missing/Deferred/Failed, and the compiler adapter. Classify the 8 compile sites (plus the `DisplayName` path) as hint or fact. Route them through the adapter; facts that come back Deferred keep today's eager `AllowIndex` behavior for now. | The real compiler path uses the new query and never initializes |
| **M3** | Lightweight ownership check (a Session's instance rejected in another Session before init or call), separate from the heavy asset audit. D-own measured about 2–3 ms for reload+call against about 1–1.6 s with a full AST/index hash, so the hash audit stays an opt-in experiment. | Misuse caught before init; normal use does not pay the hash |
| **P2** | Extract along seams: capability interfaces (forwarding helpers → hidden assertions → `vmOf`); `internal/process`; `special/`; `internal/hostbridge` (then generate the adapter family); `builtin/`; `internal/stdlib` in three steps (name registry and `bind.Env` → `hostTwin`-triggered install → lazy tables). | Root and `vm.go` each under ~3,000 lines; archcheck forbids `reflect` in `vm/` |
| **M4** + P3 | Consolidate type lookups one feature group per PR: fields and embedding → method sets and interface satisfaction → identity (TypeID; per-Session runtime interning; host-twin helpers deleted) → assignability/convertibility (shape helpers move into `semantics`) → generic inference keyed by `[]TypeID`. Then add deferred facts and `WithStrictLazyCompile` in CI, with the 15-test set as acceptance. | Consumers switched, old logic deleted, strict mode equivalent |
| **M5** | Make `PackageImage`/`PackageInstance`, source versions and derived-cache ownership concrete. Handle global derived information such as array-length folding (`runtime.arrayFolds`) explicitly. Do not remove special cases that re-parse an AST without confirming why they exist. | Reuse and invalidation explainable by owner and version |
| **P4** | VM shape in order: verifier + `MaxStack` → slot classification (stores, reads, receivers; in-place `OpNewLocal`/`OpRenewVar`) → contiguous stack → `CallSite` descriptors → inline caches. | The five frame/selector perf TODOs closed or obsolete; about 10% of chunks escaping |
| **M6** (optional) | Consider sharing compiled code. Chunks hold mutable resolution caches, function and type references, and generic binds, so this is not a natural extension of source sharing. Also optional: symbol-granular materialization, sharing bound tables across Sessions, and a tagged `Value` (default: no). | A decision is recorded |

M0–M3 are the first milestone, as in D-own. P1 sits inside it because it
is mechanical and makes M2's diffs readable.

## 5. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Large moves conflict with ongoing PRs | P1 lands file by file in short windows; moves and edits never share a PR. |
| Interning changes `%T` / `reflect.Type.String` | Rendering stays derived from the declaration; difffuzz `%T` probes pin it. |
| Interning merges distinct types that share a host carrier | TypeID keys on the represented type; a pinned case covers `atomic.Value` vs `atomic.Pointer`. |
| Compile-time answers are semantic (I5) | C3′: facts are answered, deferred or trapped, never dropped. `WithStrictLazyCompile` checks that every fact has a run-time path. |
| A deferred fact moves an error from compile time to execution | Allowed by I2; the `gate_*` cases pin the message text. |
| Session state leaks through shared assets | Recursive field allowlist in archcheck; the M3 ownership check; review checklist (D-own §16). |
| The asset audit is too slow for every call | It stays opt-in; the ownership check is the default (measured 2–3 ms against 1–1.6 s). |
| Lazy stdlib misses a bound typedef for a host value from another package | `hostTwin` lookups trigger install; a test sends `ctx.Deadline()` → `time.Time` without importing `time`. |
| Lazy stdlib changes init order or side effects | Bindings are pure construction; a test checks that two engines and a `NewSession` see identical bound packages. |
| Inline caches race on shared chunks | Atomic pointer per site; `make test-race` and `make concurrency-stress` gate P4. |
| `VMCaller` users outside the repo | `VMCaller` stays as the union. |

## 6. Explicit non-goals

- A static type checker, or rejecting ill-typed programs at compile time
  (I1/I2). Deferred facts trap on execution instead.
- Adding `Eval` to `semantics`.
- AST→AST macros (I4).
- A JIT, a register VM, or a tagged-value rewrite (I6).
- Changing script conventions or the `minigo.dev/host` stub API (I3).
- OS-level or app-level isolation between Sessions. Explicitly shared host
  maps, pointers and closures stay shared.
- A deterministic scheduler or full Go concurrency semantics.
- Process-wide type identity by pointer, or sharing compiled code (optional,
  M6).

## 7. Review checklist

D-own §16's nine questions apply to every PR in this plan, plus two from the
spike:

1. Is the new information a source-derived definition or execution state?
2. Who owns this object or cache, and for how long?
3. Does a declaration-only query stay away from init and host execution?
4. Is "not known yet" kept distinct from "absent" or "type error"?
5. Are the declaring package, file imports, type args and source version
   preserved?
6. Are identity, assignability and convertibility kept distinct?
7. Is copy versus share explicit across the host boundary?
8. Does it hold for a first run, a separate Session, a source edit and
   concurrent execution?
9. Which of lint, runtime check or behavior test enforces it?
10. *(spike)* Is this compile-time question a hint or a fact, and does the
    fact have a Deferred path?
11. *(spike)* Does it add a run-time synthesis site that bypasses the TypeID
    intern table?

## 8. Conflict decisions: D-vm against D-own

Each conflict is judged on four criteria, without a default winner:

- **E**: spike evidence.
- **I**: fit with the invariants I1–I8.
- **S**: what it makes simpler or removes.
- **M**: migration cost and risk.

| # | Point | D-vm | D-own | Comparison | Chosen |
|---|---|---|---|---|---|
| K1 | What the compiler does when it cannot answer | Unknown is always safe; the VM decides dynamically (C3) | Four result kinds; Deferred keeps a lazy reference or a run-time resolution | **E** refutes D-vm: 4 wrong outputs, 1 spurious trap, 9 lost rejections. D-own's Deferred is correct but does not say *which* questions need a run-time path. | **D-own**, refined by the spike's hint/fact split. Neither source alone was enough. |
| K2 | Strict mode | "Answer only what is already `Indexed`"; results must be identical | Policy `IndexedOnly` exists as a query policy, with no equivalence claim | Identical in mechanism. **E**: without deferred facts, either one fails 13 tests and depends on load order. | **Merged**. `IndexedOnly` is the policy; equivalence is claimed only after every fact has a Deferred path. |
| K3 | Type identity | One interned `*Type` per `reflect.Type`; `==` everywhere | `TypeID` from declaring source, position, type args and source version; pointers may differ per Session | **E** refutes keying on `reflect.Type` (`atomicBox`, timer boxes). D-vm's pointer `==` cannot work across Sessions or source versions; D-own's TypeID can. D-vm is cheaper inside one Session (**S**). | **D-own** for identity. D-vm's interning survives as a per-Session `TypeID → *TypeDef` table, a fast path under D-own's key. |
| K4 | Twin and shape helpers | All collapse to `==` | Do not merge code because it looks alike; keep identity, assignability, convertibility and satisfaction separate | **E**: 5 of 9 helpers collapse, and the other 4 implement distinct Go rules. D-vm over-claimed. | **D-own**. |
| K5 | Does the type layer import `runtime`? | Yes: `types → runtime`, with a `Def *runtime.TypeDef` back-pointer for gradual migration | No: `semantics` must not depend on runtime, VM or compiler | D-own: C1 is then enforced by the import graph, and inspect/minireflect can share answers without a VM (**I**, **S**). D-vm migrates more cheaply (**M**). The spike shows these answers decide semantics (S2), so a layer testable without a VM is worth the adapter cost. | **D-own**. D-vm's migration technique (one call site at a time; `TypeDef` gains a `TypeID` field) is kept, but as an execution-side adapter, not a back-pointer inside semantics. |
| K6 | Where generic inference lives | Whole thing in `types/infer.go` | Grows into semantics feature group by feature group | `inferCached` consumes run-time values, so a pure-semantics placement is impossible as is. | **Split** (§2.3): unification in semantics; value → TypeID in the adapter. Neither source had this. |
| K7 | Untyped-constant arithmetic | A separate `constant/` package below `vm` | Semantics answers only what execution needs, with no evaluation | Arithmetic on run-time `UConst` values is evaluation, not a declaration question. | **D-own's boundary**: arithmetic stays on the execution side. `semantics` answers only "is it a constant, and of what declared type". |
| K8 | Package layout | Public `types/`, `loader/`, `hostffi/`, `stdlib/`, `vm/sched/` | `internal/{source, semantics, execution, process, hostbridge, stdlib, archcheck}` | `internal/` keeps transitional APIs from becoming I7 obligations (**I**, **M**). D-vm's names describe mechanisms; D-own's describe ownership, which matches the review checklist. | **D-own**, with one exception: `special` stays public (I7) — but only the value-free `SymbolID`/`QuotedCall` live there; `SpecialFunc`/`SpecialContext` stay in `runtime` (a move would cycle on runtime values). Examples use the `runtime.*` surface. The registry implementation lives in `internal/execution`; the VM-side context implementation stays in `vm`. |
| K9 | Bound tables | Lazily built, potentially shared across engines | Bindings inherited at `NewSession`; per-Session state needs an explicit factory | **E**: about 45 closures capture engine state, so sharing is impossible without moving that state. D-own's factory is exactly the missing piece. D-vm's laziness is what the session cost (60–70%) calls for. | **Both**: D-own's ownership model, with `bind.Env` as the factory, plus D-vm's lazy per-package build. Cross-engine sharing is deferred to M6. |
| K10 | Architecture enforcement | `archtest_test.go`: import graph and file-size ratchet | `internal/cmd/archcheck`: import graph, mutable globals, and a recursive field allowlist on shared assets | D-own catches Session state leaking *into* shared assets, which an import graph cannot see. D-vm's size ratchet is orthogonal. | **D-own**, plus D-vm's file-size ratchet. |
| K11 | Runtime checks | None (review and tests) | A lightweight ownership check by default; the heavy asset audit is opt-in (measured 2–3 ms against 1–1.6 s) | D-vm had nothing here; D-own's numbers justify its split. | **D-own**. |
| K12 | Order of work | File split → package extraction → type model → VM shape | M0 pin → M1 ownership split → M2 semantics → M3 checks → M4 consolidation → M5 image/instance → M6 optional | D-vm extracts packages before the ownership boundary exists, so they would be renamed or moved again. D-own leaves the VM-shape work and the stdlib cost unaddressed. Slot classification needs receiver-kind *facts* (§2.5), so it must come after M4. | **D-own's skeleton** with D-vm's P1 inside the first milestone (mechanical, eases M2 review), P2 after M3, and P4 after M5 (§4). |
| K13 | Compiled code sharing | Implicitly assumed (shared chunks with atomic inline caches) | Optional M6; Chunks hold Session-dependent references | Inline caches that hold resolved cells make a Chunk Session-dependent; D-vm overlooked this. | **D-own**: Chunks are per Session until M6 decides otherwise. D-vm's atomic caches stay, for goroutines within one Session. |

Where D-vm was chosen or kept:

- VM shape (§2.5): D-own does not address frames, slots or stacks.
- Capability interfaces (§2.2).
- The lazy stdlib build and generation plan (§2.7).
- `go env` memoization.
- The verifier and Disasm goldens.
- `special` staying public.

In these areas D-own is silent or compatible, and the spike supports D-vm
with corrections.
