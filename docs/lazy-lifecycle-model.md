# Lazy lifecycle: implicit state machines made explicit

This document extracts the state transitions that the lazy package
loading and initialization machinery implements implicitly, so they can
be reasoned about — and model-checked — as explicit models. The Quint
specification in [spec/lazypkg.qnt](../spec/lazypkg.qnt) is a direct
transcription of the model below.

The machinery lives in three entangled state machines:

1. the **package load/init lifecycle** (`runtime.Package.state`)
2. the **import materialization once** (`runtime.ImportRef`)
3. the **function lazy compilation once** (`runtime.Function`)

## 1. Package lifecycle

`runtime.State` declares five stages: `Parsed`, `Indexed`,
`Initializing`, `Ready`, `Failed`. A `Package` is born at `Parsed`
(`Engine.newPackage` calls `SetState(Parsed)` before publishing it). The
lifecycle is:

```
                 (parse error)  (index error)
                     ↘             ↘
Parsed → Indexed → Initializing → Ready
                     ↘
                      Failed
```

Explicit transitions and their triggers:

| From | To | Trigger | Site |
|---|---|---|---|
| — | Parsed | `Engine.newPackage` publishes the shell | `minigo.go` |
| Parsed | Failed | file parse error | `minigo.go` `loadPathFrom` |
| Parsed | Failed | index error (redeclaration, lang gate, cgo) | `minigo.go` `indexFiles` |
| Parsed | Indexed | index built; import scopes wired; `Bootstrap`/`ConstBootstrap` hooks attached | `minigo.go` `indexFiles` |
| Indexed | Initializing | first `EnsureReadyRun` that wins `initOnce` | `runtime/package.go` |
| Initializing | Ready | `Bootstrap` returns nil | `runtime/package.go` |
| Initializing | Failed | `Bootstrap` returns error or panics | `execution.Init` fail callback |
| Ready | — | terminal for initialization | |
| Failed | — | see the terminality caveat below | |

**Terminality caveat** — `Failed` is absorbing only for *init-time*
failures, where `initOnce` has been consumed and later `MemberV` calls
re-return the cached error. A package that failed during *loading*
(parse/index errors) never ran `initOnce`, so a member access would
enter `EnsureReadyRun` for real — in practice such a package has no
usable index, but the state machine itself does not forbid the
transition. `Failed` is really two states sharing one name.

Implicit rules not visible in the enum:

- **Monotonicity** — except for the jump to `Failed`, the stage never
  regresses. Nothing enforces this; it holds only because no code path
  stores an earlier stage. `SetState` is public and unconditional.
- **`Failed` is absorbing, yet `EnsureReadyRun` still runs its body** —
  the `initOnce` is already consumed, so `Run` returns the cached error
  without re-executing. The `MemberV` fast path on `Failed` therefore
  never re-enters bootstrap; the extra `EnsureReadyRun` call exists only
  to surface the cached error.
- **The indexed gate is a shadow state** — `IndexGate` (marked /
  finished) is orthogonal to `state`. A package can be `Parsed` with
  the gate open (readers block) or `Failed` with the gate closed. The
  invariant "gate closed ⟺ indexing settled one way or the other" is
  maintained only by `defer p.FinishIndexing()` in `indexFiles` and the
  parse-error path in `loadPathFrom`.

## 2. Orthogonal sub-state: const-only initialization

`constOnce` / `ConstBootstrap` implement a **second, hidden state
machine** that never appears in `Package.state`:

```
consts-unbound --(EnsureConstsRun success)--> consts-bound
```

A package may serve constants to host-side callers while its vars and
`init()` functions have never run — the `Ready` stage is bypassed
entirely. The coupling rules are implicit in `MemberV`:

- host-side access (`run == nil`) to a const member tries the const-only
  path first and may return a value while `state` is still `Indexed`;
- in-VM access (`run != nil`) to any var/const member **upgrades** to
  full initialization first — Go's guarantee that an imported package is
  fully initialized before use;
- the upgrade is skipped when `state == Initializing`: that is an init
  cycle in progress on this very goroutine, and re-entering `initOnce`
  would deadlock. The in-progress (partial) cell is served instead.

## 3. Import materialization

Each `ImportRef` is a once-cell: unmaterialized → materialized, with
exactly one load attempt. `Peek` is the non-triggering read. The blank
imports of a package are materialized and fully initialized at the head
of `Bootstrap` — before the package's own initializer runs.

## 4. Function lazy compilation

`runtime.Function` is born uncompiled (`Chunk == nil`, `Compile` hook
injected at materialization). `EnsureCompiled` compiles exactly once;
the error is cached and re-returned. This is independent of the package
lifecycle — a function materializes under `LazyInit` without the package
ever initializing, and compiles only when first called. Compilation of a
function body may itself trigger member access on other packages
(strict-lazy mode defers unresolved facts to run time instead), which is
where the compile-time/init-time interleaving comes from.

## 5. Init cycles

`Bootstrap` executes on the triggering goroutine's VM. When package A's
initializer reads a member of B, B initializes on the same goroutine
before A continues. A cycle A → B → A terminates because the second
access to A observes `state == Initializing` and serves the partial
globals — the access is **not** blocked on `initOnce`. The correctness
invariant is:

> no goroutine ever blocks waiting for an init it itself is performing.

Note that gc never faces this situation: it **rejects import cycles at
compile time** (`import cycle not allowed`), so "Go's cyclic-init
partial-read semantics" is not a gc behavior — it is the semantics
minigo's lazy loader exhibits because it tolerates cyclic imports at
all. The invariant above is therefore a requirement on minigo, not a
compatibility property.

**The invariant is violated today for not-yet-bound members.** The
partial-serve fast path only applies when the member cell already
exists in `Globals` (runtime/package.go:468-489). A cyclic read of a
var whose cell is not yet bound — `var Y = b.ReadY()` with `ReadY`
reading back `a.Y` — falls through to `EnsureReadyRun`
(runtime/package.go:529), which re-enters the `sync.Once` inside
`execution.Init` on the same goroutine and **deadlocks** (confirmed by
experiment: the process hangs in `sync.Once.doSlow`, reached through
`MemberV` → `EnsureReadyRun` → `Once.Do`). Tracked in TODO.md.

## Invariants extracted for verification

- **INV1 monotonicity**: along the order Indexed < Initializing < Ready,
  the stage never decreases; `Failed` only reached from Initializing or
  the load phase.
- **INV2 terminality**: `Ready` and `Failed` are absorbing.
- **INV3 at-most-once init**: `Bootstrap` executes at most once per
  package, however many accessors race.
- **INV4 serve-safety**: an in-VM member access returns a value only
  when the package is `Ready` or `Initializing`.
- **INV5 deadlock freedom**: in a cyclic import graph, every initiated
  bootstrap eventually completes (`Ready` or `Failed`). NOTE: this is a
  liveness property and is *not* machine-verified — `quint run`/`verify`
  check state invariants (safety) only. What the simulation does pin is
  the reachability direction: traces exist in which cyclic inits
  complete (and the `noPartialServeYet` witness shows the partial-serve
  path is exercised). A cycle of repeated partial serves that never
  completes is not excluded by the checked invariants — and, per the
  not-yet-bound-member finding above, the implementation today can in
  fact deadlock, so INV5 is known-false until the TODO entry is fixed.
- **INV6 host const fast path**: a const may be served host-side before
  full init, but in-VM service of the same member implies `Ready` or
  `Initializing` afterwards.

## What this suggests for the production code

The enum exists but the transitions are scattered across `minigo.go`,
`runtime/package.go` and `execution.Init`, and two of the state machines
(const-only init, indexed gate) live outside the enum entirely. A
follow-up refactor could centralize transitions in one function
(`p.advance(to)` with a legality table) and make the const-bound flag an
explicit field. That rewrite is deliberately *not* part of this change —
the Quint model first pins the behavior the refactor must preserve.
