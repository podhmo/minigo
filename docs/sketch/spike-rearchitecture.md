# Spike: checking the assumptions behind the rearchitecture plan

Status: spike, measured on main `a3f4e24` (2026-11-17). All
instrumentation was throwaway and is not part of this change; this file
records the method and the numbers so the runs can be repeated.

Question: the rearchitecture plan
([plan-rearchitecture.md](https://github.com/podhmo/minigo/blob/claude/vigilant-cannon-el2lp8/docs/sketch/plan-rearchitecture.md),
discussion #1090) rests on several claims that were reasoned from the code,
not measured. Which of them hold?

Japanese version: [ja/spike-rearchitecture.md](./ja/spike-rearchitecture.md).

## Summary

| # | Plan claim | Verdict | What the plan should change |
|---|---|---|---|
| S1 | C1: compiling only reaches `Indexed`, never `Initializing` | **Holds** (0 violations) | Nothing; it can be enforced as planned |
| S2 | C3: an *unknown* compile-time answer is always safe; `WithStrictLazyCompile` gives identical results | **False** | Split compile-time knowledge into *hints* and *facts*; facts must be answered or deferred to run time, never dropped |
| S3 | `chunkCellsEscape` is true for nearly every chunk; store targets are the cause | **Overstated** (45%, mostly field reads and receivers) | Slot classification must cover selector reads and call receivers, and `OpNewLocal`/`OpRenewVar` must stop allocating |
| S4 | Slot classification makes most chunks escape-free | **Holds** (45% → ~10%; ~5% of locals Boxed) | Special-form call sites force every visible local Boxed |
| S5 | `VMCaller` splits into small capability interfaces | **Holds, with more surface than listed** | 5 hidden capabilities reached by interface assertion; 184 functions only forward the caller |
| S6 | Every engine pays `installStdlib`; lazy per-package install is worthwhile | **Partly** | Big for `NewSession`, minor for `NewEngine` (dominated by `go env` subprocesses); 4 couplings to handle |
| S7 | One `*types.Type` per `reflect.Type`; twin/shape heuristics all collapse to `==` | **Partly** | Key on the represented type, not `reflect.Type`; 5 of 9 helpers collapse, 4 encode Go identity rules |

## S1, S2: the compile-time laziness contract (C1–C3)

### Method

- Route the 8 `ref.Materialize()` calls in `compile/compile.go` through one
  function (the future oracle). An env var selects its mode:
  - `off`: unchanged behaviour, but logs each first materialization.
  - `none`: never answers. This is the strongest form of C3.
  - `peek`: answers only when the ref was already materialized by
    someone else. This is the planned `WithStrictLazyCompile`.
- Log `compile-build` when `Engine.buildPackage` runs with a
  `minigo/compile.` frame on the stack. Log a C1 violation when
  `EnsureReadyRun` or `EnsureConstsRun` starts a bootstrap from such a stack.
- Run `go test ./... -count=1` plus all examples in each mode, and diff the
  failing test names against `off`. Three tests fail in every mode because
  the sandbox runs as root: `os_err_script_path` and `TestREPLLoadErrors`
  expect `permission denied`.

### Results

- **C1 holds.** No initialization ever started from a compile stack across
  the whole suite and examples.
- Compile-triggered loads are real but modest. In the root module, 162 of
  2,502 package builds (6.5%) started from the compiler. Most were small
  host-side packages: `io/fs`, `mime/multipart`, `net/http`, `net/url`,
  `math/bits`.
- **C3 is false.** `none` adds 15 failures and `peek` adds 13:

| Kind | Tests | Why |
|---|---|---|
| Wrong output (I5 violation) | `assign_pkgname_differs`, `litkey_named_peel`, `src_pkgtype_maplit_keys`, `shift_const_count` (`none` only) | Composite-literal key typing, assignment through a package whose name differs from its path, and the shift-count constness flag (`ShiftConstCount`) all read the imported package |
| Spurious trap | `TestNetHTTPRoundtrip` (`none` only) | `rc == NoBody` traps `mismatched types io.ReadCloser and noBody`: without the member's declared type, the comparison is not lowered to an interface compare |
| Lost gc rejection | 8 `gate_*`, `selconv_typearg`, `selnilable_trap` | The run succeeds where gc reports `mismatched types time.Duration and int64`, etc. |
| Unit test | `compile.TestSelMemberSyntheticTypeEntry` | Tests the lookup directly |

- `peek` still breaks 13 tests, so "answer only for what is already
  `Indexed`" changes behaviour depending on load order. A mode whose results
  depend on load order cannot be a CI equivalence check.
- Root cause: today's compile-time lookups are not only speed hints. Some
  decide semantics: constness (`isConstExpr`, `certainlyNonConst`), the
  nilability of qualified types (`selNilable`), static operand types for
  equality lowering, and literal key types.

### What the plan should say instead

- **C3′.** Compile-time knowledge comes in two kinds:
  - *Hints* only make things faster. Dropping them is safe.
  - *Facts* decide semantics. Each fact must either be answered, by loading
    to `Indexed` (allowed by C1), or be emitted as a deferred check that the
    VM resolves at first execution. A deferred check carries a `types.Ref`
    in the `CallSite`.
- `WithStrictLazyCompile` then means "every fact is deferred". That is a
  meaningful equivalence test. "The oracle says unknown" is not.
- Phase 3 step 5 should start by sorting the 8 call sites (plus the
  `runtime/typedef.go` ones the compiler reaches through `DisplayName`)
  into hint or fact.

## S3, S4: slot classification

### Method

Tag each `OpLocalRef` emission with its reason in a side table on the chunk.
When the VM first computes `chunkCellsEscape`, log the chunk. A runtime
probe also records pointer-receiver binds of frame-local cells. The runs
cover the root suite and 6 examples: 4,161 deduplicated chunks (11,402 raw).

### Results

- Chunks with `chunkCellsEscape` true today: **44.5%**, which is 56% of
  chunks that have locals at all.
- Reasons for `OpLocalRef`:

| Reason | Share | Escapes? |
|---|---:|---|
| selector read `x.f` / method value `x.M` | 35.9% | No, except a method value with a pointer receiver |
| call receiver `x.M()` | 34.7% | Only when a pointer receiver binds the cell (31 chunks at run time) |
| `x = v`, `x.f = v`, `x[i] = v`, multi-assign, `op=`, `*p = v`, incdec | ~27% | No |
| `&x`, `&x.f`, `&x[i]` | 2.2% | Yes |

- Escaping chunks under precise classification:

| Rule | Escaping chunks | Boxed locals |
|---|---:|---:|
| only `&`, closure capture, special form | 9.3% | 4.7% |
| plus pointer-receiver binds seen at run time (**realistic**) | **9.7%** | **5.0%** |
| plus every `x.M()` receiver (pessimistic upper bound) | 27.9% | 15.1% |

### Corrections to the plan

1. The cause is not stores. About 70% of `OpLocalRef` comes from field reads
   and call receivers, because `selectorBaseIsVar` is true for any variable.
   Field reads must become `OpLocal` + `OpSelect`. `x.M()` needs the
   receiver kind, either statically (a *fact* in the S2 sense) or through a
   run-time fallback.
2. An escape-free chunk saves less than the plan assumes. `OpNewLocal`
   (vm.go:2597) allocates a fresh `Cell` on every `:=`/`var`, and
   `OpRenewVar` (vm.go:2607) allocates one per loop iteration. The #1024
   block reuse therefore only covers parameters. Plain slots must change
   these two ops to write in place.
3. Special forms: `trySpecial` (compile.go:6641) snapshots every visible
   local into `QuotedCall.Locals`, and `specialCtx.Eval` (vm.go:17576)
   shares the caller's cells. Every local visible at a special-form call
   site must be Boxed. Only 25 chunks contain one.
4. Other code that reads frame cells generically and needs porting:
   - `selectMember` / `structMember` `case *Cell` (vm.go:3640, 7552)
   - `assignRef` (vm.go:1156)
   - `specialCtx.Resolve` (vm.go:17409)
   - `localTypedefOf` (vm.go:8886)
   - named results (vm.go:2182, 3356)
   - range iterator slots (vm.go:3218)

   None of these are blockers. `OpEvalAST`, `goto`, `defer`/`recover` and
   `inspect` are unaffected.

## S5: splitting `VMCaller`

### Method

A `go/ast` scan of every non-test function or func literal with a
`VMCaller` parameter: which methods does it call on that parameter?

### Results

- 360 functions take a `VMCaller`:
  - 96 never use it.
  - 184 only forward it to a helper (`runtime.IfaceMember`, `hostErrOf`,
    `display`, `callOrPanic`, `goJSONSeen`, …).
  - 80 call methods on it directly.
- Methods by number of functions calling them: `Call` 24, `TypeOf` 16,
  `Convert` 13, `ElemZero` 12, `Zero` 10, `ElemOf` 6, `Copy` 5, `Equal` 4,
  `ResolveType` 4, `NewMap` 3, `Member` 3. The rest are at 2 or fewer.
  `Task` has no non-test caller.
- **Hidden surface.** Callers also type-assert the `VMCaller` to anonymous
  interfaces for `ElemZero` (×3), `HostClear`, `HostMapDelete`,
  `MaterializeConstErr`, `HostNilTwin` and `CallCatching`. `vm.vmOf`
  (vm.go:4994) downcasts to `*VM` 7 times.

### Verdict

- The split is feasible, and most bindings need nothing or only `Caller`.
- The capability set must include the hidden assertions. Otherwise moving
  bindings out of the root still drags a concrete `*VM` along.
- The forwarding helpers decide the real requirement of 184 functions, so
  give the helpers capability-typed parameters first.

## S6: per-package lazy stdlib install

### Measurements

4 CPUs, noisy shared machine:

| Benchmark | time/op | B/op | allocs/op |
|---|---|---|---|
| `installStdlib` alone | 0.76–0.87 ms | 425 KB | 4,203 |
| `NewEngine` | 6–11 ms | 566 KB | 4,437 |
| `NewSession` | 0.57–1.0 ms | 440 KB | 4,273 |
| trivial run via `NewEngine` | 11–14 ms | 593 KB | 4,655 |
| trivial run via `NewSession` | 1.1–1.4 ms | 451 KB | 4,429 |

- **Engine path.** `installStdlib` is about 7% of wall time but 90% of
  allocations. Wall time is dominated by two `go env` subprocesses (`GOROOT`,
  `GOMODCACHE`) that `pkg/locator` runs for every engine
  (locator.go:133–138, 474–490). Memoizing those is the cheaper and bigger
  win.
- **Session path.** `installStdlib` is 60–70% of wall time and about 94% of
  allocations. Here the claim holds.
- The suite binds 45 import paths and 1,134 members. A testdata or example
  file imports 1.92 bound packages on average.

### Couplings a lazy registry must handle

1. `hostTwin` (minigo.go:314) finds bound typedefs through
   `e.pkgs[rt.PkgPath()]` for host values flowing out of *other* packages,
   such as a `time.Time` from `ctx.Deadline()`. A lazy design must install on
   that lookup too, or it silently returns nil.
2. `BoundPackages`/`BoundSymbols`/`BoundOf`, `vet.go:196`, `SourceOf`,
   `NewSession`'s bind precedence and `loadPathMode`'s bound-shadow check all
   read `e.binds`/`e.pkgs` eagerly. They need a name registry that is
   separate from the built tables.
3. Engine state captured in closures: `e.fsPath` ×17, `e.cwd` (written by
   `os.Chdir`), `e.args`, output, and `AllowedRoots` deciding the `os` and
   `host` tables. Lazy install still rebuilds per engine. Sharing tables
   across sessions would need that state to move into the call context.
4. The global `templateFuncMap` TypeDef is shared by every engine, and
   `Bind` sets `td.Pkg` only when it is nil (minigo.go:787). So the first
   engine's package pointer sticks for all later engines (tracked in
   TODO.md).

### Generation

- About 50–55% of the 869 entries are shapes `gen-intrinsics` could emit.
- About 12% are typedefs carrying method lists or `HostScalar`, which the
  generator does not produce today.
- About 25% are real adapters.
- `WrapFunc` reflects on every call, so generated binds would be slower than
  the current direct wrappers. This is acceptable under I6, but the plan
  should say so.

## S7: interning host types

### Results

- One-`*TypeDef`-per-`reflect.Type` breaks mostly because the bindings are
  rebuilt per engine: 99 host types got one fresh TypeDef per engine, across
  1,035 engines and sessions.
- Inside a single engine, duplicates come from uncached run-time synthesis:
  - `vm.hostTypedefOf` (vm.go:18295) and `hostPkgElemTypedef`
    (vm.go:18347) mint a fresh TypeDef per call. `ast.Expr` got 163
    synthesized copies.
  - 14 types had both a bound TypeDef and synthesized ones.
  - 26 types had a source-declared twin plus synthesized ones.
- A twin is never one package bound and interpreted at once. It is a bound
  package returning a value whose type is declared in a package that is
  interpreted from source. Examples: `os` returning `*fs.PathError` while
  `io/fs` runs from source, or host `go/ast` nodes meeting source `go/ast`.
  Unifying the type is correct. The value representations still differ
  (`*runtime.Struct` against `GoValue`), so the conversion layer stays:
  `hostErrTwins`, `goNative` and the proxy boxes.
- **Counterexample to keying by `reflect.Type`.** `atomicBox` backs both
  `atomic.Value` and `atomic.Pointer`, and `timerChanBox`/`afterFuncTimer`
  stand for `*time.Timer`. The intern key must be the *represented* type,
  (PkgPath, Name), not the Go type of the box.

| Helper | Collapses to `==`? |
|---|---|
| `hostTwinIs`, `hostNilTwin`, `sameSpelledTwin`, `HostNamedIs`/`HostTwinOf`, `sourceTwinOf` | Yes, with per-engine interning by (PkgPath, Name) and with synthesis routed through it |
| `peelAlias` | Only if aliases resolve at creation, which conflicts with laziness |
| `tdShapeEq`, `underlyingShape`/`convShapeEq` | No: they implement Go's identical-underlying rules for different types |
| `structFieldsEq` | No, short of full structural hash-consing |

## Follow-ups this spike suggests, in order

1. Memoize `go env GOROOT`/`GOMODCACHE` in `pkg/locator`. This is small,
   independent and the largest single `NewEngine` cost.
2. Rewrite plan §2.4 as C3′ (hints against facts), and make
   `WithStrictLazyCompile` mean "defer all facts".
3. Rewrite plan §2.5.1 to classify selector reads and receivers. Make
   `OpNewLocal`/`OpRenewVar` write in place for Plain slots.
4. Add the hidden assertions to the §2.2 capability list, and type the
   forwarding helpers first.
5. Rewrite plan §2.3 to intern per engine by the represented (PkgPath, Name),
   and drop "all twin/shape heuristics collapse".
6. Add the four lazy-registry couplings (S6) to §2.7, and justify lazy
   install by `NewSession` cost.
