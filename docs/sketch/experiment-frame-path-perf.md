# Experiment: where does grafana-openapi's wall time go?

Status: step 3 is proposed in #624 (`perf/global-site-cache`). The
other steps live on local experiment branches and are not necessarily
for merge: `experiment/frame-alloc-batch` (step 1) and
`experiment/iface-cache-bound` (step 4, upper-bound code, not mergeable
as is). Commit hashes below refer to those experiment branches.

Question: the realworld profile keeps flagging `prepFrame` as the top
allocator. Is reducing frame-path allocation the right lever for wall
time, and if not, what is?

This report is updated after every step. Each step records the method
so it can be re-run, the numbers, and the verdict.

## Verdict so far

Allocation is not the wall-time bottleneck on a multi-core machine. The
interpreter itself is effectively single-threaded and GC mark work runs
on idle cores, so GC costs only ~4% of wall. Cutting allocations helps a
lot only when the process is CPU-constrained (`GOMAXPROCS=1`: GC adds
+38%). The wall-time levers are the main-thread hot paths.

Two of them paid off:

- Caching global-name resolution per instruction site (step 3) cut
  grafana-openapi by 17% and the micro probe by 30%.
- Memoizing interface satisfaction (step 4, upper bound) cut a further
  7–11%. This is the same cost the profile showed as `coerce` and
  `methodWalkU`.

Cumulative on grafana-openapi, main → step 4: 3.279s → 2.365s
(−27.9%); with `GOMAXPROCS=1`, 4.498s → 3.391s (−24.6%). After step 4
the remaining profile is flat: no node outside the loop itself exceeds
~5% of CPU, so further single-site work falls below measurement noise
(±2–3% at 9–11 rounds).

## Step 0: the alloc_space regression from the difffuzz stack (landed)

TODO entry: alloc_space grew ~100 MB on the frame path across the
difffuzz fix stack (#547..#558), with `massign_pinbase` suspected.

- `massign_pinbase` (#555) allocates nothing; it only re-points a ref
  base at `c.Elem`.
- Line-level alloc profiles (`0bc8a3c1` before the stack vs. `e891b090`)
  put the growth on non-param cells in `prepFrame` (0 → 62 MB) and
  `OpNewLocal` cells in `loop` (26 → 63 MB). Cause: #551's two-phase
  binary operands hoisted every call operand into a `$bin` scratch
  local, even for `fib(n-1) + fib(n-2)`, where sequential evaluation is
  already in two-phase order.
- #617: `hoistEagerOps` skips hoisting when every expr up to the last
  eager one is that op alone.
- #618: `assignCell` skips the UConst round trip when a bare scalar is
  stored into a cell holding the same bare kind (the re-typing is the
  identity there).

Micro probe (realworld `probes/micro`):

| | alloc_space | wall |
|---|---|---|
| before the difffuzz stack | 688 MB | 0.74s |
| main before the fixes | 813 MB | 0.83s |
| #617 | 681 MB | 0.79s |
| #617 + #618 | 640 MB | 0.75–0.77s |

grafana-openapi (7 interleaved rounds, median): main 3.388s, #617
3.413s, #618 3.334s — within noise. alloc_space −4% to −7%.

## Step 1: batch frame allocations (this branch, `052a37ef`)

`prepFrame` paid one allocation per local cell plus `append` regrowth
of the operand stack (1→2→4→8) on every call.

- `bytecode.Chunk.StackHint` (`atomic.Int32`, shared by goroutine VMs)
  records the stack capacity a finished run reached; `prepFrame`
  presizes the next frame's stack with it.
- A frame's initial cells share one `[]runtime.Cell` backing array.
  Retention cost only: a closure capturing one cell keeps the array
  alive.

grafana-openapi, 15 interleaved rounds:

| | wall median | alloc_objects |
|---|---|---|
| main | 3.252s | 58.0M |
| step 1 | 3.205s (−1.4%) | 48.4M (−17%) |

`frame.push` regrowth (230 MB) is gone, replaced by a 194 MB presized
stack in `prepFrame`. Cells: 256 MB → 237 MB.

Rejected variant: inline `[8]Value` stack and `[8]*Cell` locals buffers
inside `frame`. alloc_objects −27%, but alloc bytes +6% and wall
3.199s, no better than step 1.

Verdict: a modest win in object count, a small win in wall. Kept on the
branch, not proposed.

## Step 2: how much of wall is allocation at all?

Method (grafana-openapi, main binary):

- `GOGC=off` vs. default, interleaved, plus `time` for user/sys/real.
- `GODEBUG=gctrace=1` for GC count and CPU.
- `GOMAXPROCS=1` to see the single-core cost.
- CPU profile from the realworld `prof` harness, split by `-focus`.

| setting | wall | note |
|---|---|---|
| default (GOGC=100) | 3.26s | 168% CPU; GC uses ~2.4s CPU on other cores |
| GOGC=off | 3.12s | 99% CPU; the mutator is ~3.1s single-threaded |
| GOGC=200 / 400 | 3.11s / 3.06s | |
| GOMAXPROCS=1 | 4.52s | GC competes for the one core: +38% |
| GOMAXPROCS=1, GOGC=400 | 3.48s | |

gctrace: 289 GCs, ~2.6s GC CPU, ~0.47s of concurrent GC wall.
Startup and compilation cost ~0.01s, which is negligible.

Main-thread CPU (approximate; the deep interpreter recursion truncates
pprof stacks at 64 frames, so treat cum values as indicative):

| node | CPU |
|---|---|
| VM loop (total) | 1.92s |
| `coerce` | 0.36s |
| `selectMember` | 0.34s |
| `resolveGlobalE` (by-name map lookup per global read) | 0.32s |
| `assignRef` | 0.20s |
| `ifaceSatisfied` → `methodWalkU` | 0.19s |
| `prepFrame` | 0.12s |
| `runtime.Tag` | 0.07s |
| heap growth `madvise` (outside the loop) | 0.37s |
| goroutine stack growth (`morestack`) | 0.10s |

Caveat: the `prof` harness records an alloc profile, and that
bookkeeping (`profilealloc`) costs ~0.3s CPU by itself. Profiles read a
little heavier than plain runs.

Hypotheses from step 1, judged:

1. `&frame` allocation: weak. `prepFrame` is ~4% CPU and its GC cost
   runs off-thread. A frame free list is worth a few percent at most on
   multi-core; it matters for single-core deployments.
2. `methodWalkU`: medium. A CPU cost (interface satisfaction checks),
   not an allocation one, ~6%. A memoization candidate.
3. `runtime.Tag`: weak, ~2%.

## Step 3: per-site cache for global-name resolution (`09bc241b`)

Every `OpGlobal` re-ran `resolveGlobalE`. That meant `fileOf`'s
`Fset.PositionFor`, the file-scope map, and `Globals` under its
RWMutex. For builtins it also walked the index, renamed imports and
dot imports before reaching `Builtin`.

Upper bound first: an unsafe per-VM map cache keyed by (chunk, ip),
with no invalidation. It gave 3.210s → 2.667s (−17%) with identical
output, far more than the 0.32s the CPU profile attributed. Truncated
stacks under-count the hot path.

Design of the real version:

- The compiler gives each `OpGlobal` a `bytecode.GlobalSite`
  (`Chunk.Sites`, indexed by the instruction's C operand). The
  `atomic.Value` is shared by goroutine VMs.
- A slot is valid while the package's `Globals` generation is
  unchanged. `runtime.Env` bumps it on `Set`/`Delete`/`Touch`. The REPL
  calls `Touch` where it rewrites `Scopes`/`Imports`/`Index`, before
  running init.
- Package vars cache their cell, and reads stay live. Dot-imported
  names live in another package's env, so they are never cached.
  Neither are builtins in files that have a dot import.

The first real version reached only 2.904s (−9.8%). Counters showed
2.38M of 8.3M site reads were invalidated. `selectMember`'s ImportRef
case and the dot-import path re-`Set` the value `MemberV` had just
read from `Globals` on every access: 1.4M Sets, each bumping the
imported package's generation. Guarding those to cache only fresh
materializations closed the gap:

| | grafana-openapi (11 rounds) | micro (5 rounds) |
|---|---|---|
| step 1 | 3.198s | 0.793s |
| step 3 | 2.644s (−17.3%) | 0.552s (−30%) |

`resolveGlobal*` CPU fell from 0.32s to 0.01s. Outputs are identical.

Test: `TestREPLGlobalSiteCache` (pin mode). A function published into
an entered package is patched in place by `Globals.Set`, so a caller
that already ran must see the patch. Plain REPL lines would not do:
they re-materialize functions on reload. The test fails when the
generation check is removed (negative control). `make test` and
`go test -race .` pass.

Verdict: strong, a merge candidate on its own (it does not depend on
step 1).

## Step 4: re-profile on step 3, then the remaining candidates

Re-profiled on top of step 3. `coerce` was still on top (0.35s), and
half of it was `satisfiesIface` → `ifaceSatisfied`, which calls
`methodSetOfValue`/`methodWalkU` and `ifaceSigsMatch`. So the
step 2 hypotheses "`coerce`" and "`methodWalkU`" are one cost: the
interface satisfaction check that runs on every conversion to an
interface type (go/parser passes `*ast.Ident` as `ast.Expr`
constantly).

### 4a. Interface satisfaction cache (`72143829`, upper bound)

A per-VM map keyed by (interface typedef, dynamic typedef,
pointer-or-not) for struct and named values, the analogue of Go's
itab cache. There is no invalidation.

- grafana-openapi: 2.640s → 2.460s (−6.8%, 9 rounds); in a later
  sitting 2.653s → 2.365s (−10.9%, 11 rounds). Output is identical.
- 349,639 hits and 57 misses. Only `IfaceNil` values (~9.5k) are
  unkeyed, so the bound is effectively reached.
- `satisfiesIface` disappears from `coerce`. What remains is spread
  out: `coerceConcrete` 0.13s, `valueCopy` 0.06s, `zeroValue` 0.05s.

To make it mergeable, the cache needs invalidation wherever a type's
method set can change after the first check. The known case is REPL
method grafting onto a published typedef. It also needs a shared
(goroutine-safe) home, like step 3's sites, or it has to stay per-VM.

Verdict: strong. The second-best lever after step 3.

### 4b. `selectMember` (0.25s), rejected below noise

The breakdown: `FieldRef.find` (0.09s; it allocates BFS `level`/`hits`
slices on every read), `structMember`'s by-name field scan (0.06s),
and `pkg.Name` resolution via `memberOf` (0.05s).

- Tried a no-allocation depth-0 fast path in `FieldRef.find`. The
  result was 2.378s → 2.381s: no measurable change. Not kept.
- A per-site cache for `pkg.Name` selections (the step 3 mechanism
  extended to `OpSelect` on `ImportRef`) would save at most ~0.05s
  (~2%), below noise. Not tried.

### 4c. Summary table (grafana-openapi, 11 interleaved rounds)

| build | wall | `GOMAXPROCS=1` (3 rounds) |
|---|---|---|
| main | 3.279s | 4.498s |
| step 1 | 3.229s | 4.575s |
| step 3 | 2.653s | 3.834s |
| step 4 (bound) | 2.365s | 3.391s |

Step 1 shows no gain at `GOMAXPROCS=1` either, within noise at 3
rounds.

## Next

The investigation is done for now. Implementation candidates, in
order:

1. Step 3: ready as is. It does not depend on step 1. Before a PR, add
   a goroutine test that shares one site, and a test that dot-imported
   names are never cached.
2. Step 4a: needs method-set invalidation (REPL grafting) and a
   decision on where the cache lives.
3. Step 1: optional. Fewer allocations, but no wall gain.
4. GC tuning: GOGC=400 saves ~6% wall (23% at `GOMAXPROCS=1`). If
   anything, change it only in `cmd/minigo`; a library should not
   change process-wide GC settings.

## How to re-run

The realworld `compare.sh` (podhmo/minigo-usecasefuzz) needs bash 4
(`declare -A`), which macOS's bash 3.2 lacks. These experiments
replicate its steps by hand:

1. Fetch the pinned grafana checkout at the `targets.tsv` SHA
   (`fa8d6e65`) with `git fetch --depth 1`.
2. Build `cmd/minigo` for each side from a scratch worktree.
3. Build the `realworld/prof` harness against each side with a
   generated `go.mod` `replace`.
4. Run `minigo run .` in `realworld/tasks/grafana-openapi` with
   `TARGET_DIR` set. Interleave sides for N rounds, take the median,
   and check that outputs are identical.
