# Experiment: where does grafana-openapi's wall time go?

Status: step 3 is proposed in #624 (`perf/global-site-cache`), and
step 4a is stacked on it (`perf/iface-sat-cache`), and step 7 on that
(`perf/fieldref-direct-scan`). The other steps live
on local experiment branches and are not necessarily for merge:
`experiment/frame-alloc-batch` (step 1) and `experiment/iface-cache-bound`
(the step 4a upper-bound code). Commit hashes in the step 1 and step 4
sections refer to those experiment branches.

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

A third came from removing an allocation, not adding a cache: a
direct-field fast path in `FieldRef.find` (step 7) cut a further ~9%.

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

## Step 5: step 4a made mergeable (`perf/iface-sat-cache`)

Analysis before implementing:

- What the answer depends on. `methodInfoOfValue` derives the method
  set from the typedef alone: `Struct.Def` or `Named.Typ`, plus whether
  the value was reached through a pointer. The one exception is a Named
  host box whose tag declares no methods. That exposes the boxed
  value's reflect methods, so it is left uncached. An "unsure"
  embedded-type result is static too, so it can be cached.
- Where a method set changes after construction. Only the REPL's
  pin-mode graft onto a live typedef (`commitWrites`). The index-side
  graft evicts the typedef from `Globals`, so the next lookup builds a
  new identity.
- Where the cache lives. A per-VM map is enough, because each
  goroutine has its own VM. A size cap (4096, restart when full) guards
  scripts that mint typedefs per call.

Invalidation: `runtime.MethodSetsChanged` bumps a global epoch. The
REPL graft calls it, and a VM drops its memo when the epoch has moved.
Every `Engine.Call` (and every REPL line) runs on a fresh VM, so the
epoch only matters for a VM that stays alive across a graft, e.g. a
long-running goroutine. `TestIfaceMemoMethodSetEpoch` therefore reuses
one VM across a direct graft. It fails without the epoch bump. A first
REPL-level test did not: it passed the negative control, because each
line got a fresh VM.

| | grafana-openapi (11 rounds) | micro (5 rounds) |
|---|---|---|
| step 3 | 2.818s | 0.568s |
| step 3 + 4a | 2.515s (−10.8%) | 0.579s (one interface conversion: no change) |

## Next

The investigation is done for now. Implementation candidates, in
order:

1. Step 3: #624.
2. Step 4a: `perf/iface-sat-cache`, stacked on #624.
3. Step 1: optional. Fewer allocations, but no wall gain.
4. GC tuning: GOGC=400 saves ~6% wall (23% at `GOMAXPROCS=1`). If
   anything, change it only in `cmd/minigo`; a library should not
   change process-wide GC settings. Decided: not pursued.
5. Field lookups: step 7, `perf/fieldref-direct-scan`. A 9-line
   direct-field fast path in `FieldRef.find`, −8.7%. The per-site
   field-index cache measured the same and is not needed.
6. A runtime fast path in `coerce` that returns early when the value
   already has the target type (~4–6% ceiling, see step 6). Small
   change, but it must first be confirmed that `coerce` has no other
   effect in that case. Not compile-time foldable: the 1.6M
   `OpConst` → `OpCoerce` pairs at frame entry are parameter coercions.

## Beyond per-site caches

With steps 3 and 4a in, ~2.4–2.5s on grafana-openapi is roughly the
ceiling of the "find a hot spot, cache it" approach. No node outside
the loop body exceeds ~5% of CPU, and single-site wins now fall below
noise. The options beyond that, and their measured ceilings (step 6),
are:

| option | what it is | measured ceiling | status |
|---|---|---|---|
| GC tuning | raise GOGC | ~6% (23% at `GOMAXPROCS=1`) | not pursued |
| static binding / quickening: globals | resolve names to slots at compile time, or rewrite the instruction into a specialized form on first execution | <1% (the step 3 cache hit path is ~0.02s/run) | nothing left |
| static binding / quickening: fields | resolve a field name to its index once per site (an inline cache keyed by the struct typedef), with no type checker needed | ~8–9% | the whole gain came from `FieldRef.find`'s allocations; step 7 gets it without a cache |
| fewer `coerce` calls | skip conversions that return their input unchanged | ~4–6% | needs a runtime type-match fast path; no compile-time sub-case |
| value representation and instruction set | unboxed scalars; superinstructions (instruction fusion) | ~13% combined, mostly allocation; fusion alone ~1% | not committed to |

Terms: instruction fusion (superinstructions) merges frequent
instruction sequences into one opcode, e.g. `OpLocal` → `OpConst` →
`OpBinary` becomes `OpAddLocalConst`. It cuts dispatch count, not name
resolution. Quickening is CPython 3.11's specializing adaptive
interpreter approach: rewrite an instruction in place into a
specialized form after it first runs. The per-site caches of steps 3
and 4a are the inline-cache step before quickening.

## Step 6: measuring the ceilings of the remaining options

Method, on top of step 4a (`perf/iface-sat-cache`):

- 3 CPU and alloc profiles from the `prof` harness, merged (~2.35s per
  run). The harness's own alloc-profile bookkeeping (`profilealloc`,
  `stkbucket`) costs ~0.22s per run and is excluded below.
- One run of a temporary counting build (not committed). It counted
  opcode frequencies, 2- and 3-opcode sequences, `coerce` calls by
  call site (and how many returned their input unchanged), and by-name
  field lookups.

Per-run figures for grafana-openapi:

| quantity | value |
|---|---|
| VM loop (main-thread CPU, cum) | ~1.33s |
| instructions executed | 98.5M (~20 ns each, all work included) |
| `loop` flat time, i.e. dispatch | ~0.15s (~6% of a run, ~1.5 ns per instruction) |
| file I/O (the script's `os.ReadFile`) | ~0.11s, irreducible |
| main-thread `mallocgc` | ~0.17s (~7%) |
| `runtime.convT*` (scalar boxing) | ~0.02s (<1%) |
| `coerce` (cum) | ~0.23s (~10%) |
| by-name field lookups: CPU | ~0.2s (`FieldRef.find` 0.13s, `structMember` 0.06s, plus `setField`'s name compare) |
| by-name field lookups: count | 18.5M (`FieldRef.find` 9.05M, `structMember` 7.07M, `setField` 2.34M) |
| cached `OpGlobal` reads | 8.28M, ~0.02s |

Opcode mix (top): `OpLocalRef` 10.0%, `OpSelect` 10.0%, `OpLocal`
8.8%, `OpGlobal` 8.4%, `OpBinary` 8.2%, `OpConst` 7.8%. Top sequences:
`OpLocalRef OpSelect` 6.8%, `OpConst OpBinary` 3.7%, `OpGlobal
OpSelect` 2.6%, `OpLocalRef OpFieldRef` 2.6%, `OpBinary OpJumpFalse`
2.4%. The top five pairs cover ~18% of instructions.

`coerce`: 8.6M calls, 61% returning their input unchanged. By site:
`OpCoerce` 4.05M (57% unchanged), `setField` 2.34M (66%), `OpCoerceTop`
1.33M (66%), `assignCell` 0.53M (56%). 1.6M `OpConst` → `OpCoerce`
pairs run as the first two instructions of a frame. These are the
function prologue's parameter coercions (`emitParamCoerces` →
`emitTypeCoerce` in `compile/compile.go`): the `OpConst` pushes the
parameter's TypeDef, and `OpCoerce` converts the bound argument to it.
The coerced value is a runtime argument, not a constant, so compile-time
folding does not apply. (An earlier draft of this report read the pair
as a constant coerced on entry; that was wrong.)

Allocation objects (3 runs): `prepFrame` 24%, `runtime.Tag` 18% (the
`Named` wrapper for typed values), `loop` 16%, `frame.push` 12% (stack
regrowth, see step 1), `popArgs` 9%.

Reading the ceilings:

- Static binding for fields is the largest remaining single lever,
  ~0.2s (~8–9%). A per-site inline cache from struct typedef to field
  index works like steps 3 and 4a and needs no type checker.
  `FieldRef.find` also allocates its BFS slices on every read. Treat
  the figure as optimistic: step 4b's fast path removed those
  allocations (not the name scan) and gained nothing measurable.
- Skipping `coerce` calls that change nothing is worth ~4–6% (61% of
  ~0.23s, minus the value copy those calls still need). Proving it at
  compile time needs a type checker. The realistic form is a runtime
  fast path at the top of `coerce` (e.g. the struct's typedef already
  equals the target), which covers every call site, the prologue
  included. Its cost is reading `coerce` to confirm the early return
  is equivalent (no cell typing or other side effect is skipped).
- Option 4 is ~13% combined: dispatch ~6% plus main-thread allocation
  ~7%. That is below the ~15% guideline, and scalar boxing is
  negligible (<1%). Fusion by itself saves at most ~18% of dispatches,
  so ~1% of wall, unless a fused opcode also skips intermediate work
  (e.g. `OpLocalRef OpSelect` not materializing a ref). The value-
  representation cost that does show is `runtime.Tag`'s `Named`
  wrapper, 18% of allocated objects.

## Step 7: field lookups (`perf/fieldref-direct-scan`)

Method: an upper-bound build added a per-site field-index cache
(`bytecode.FieldSite`, keyed by the struct typedef, depth 0 fields only)
to all three by-name lookups: `OpSelect`, `OpSetField` and `OpFieldRef`
(`FieldRef.find`). Then one build per lookup kept only that cache.
Interleaved rounds on grafana-openapi, on top of step 4a. Outputs were
identical on every side.

| build | median vs step 4a |
|---|---|
| all three caches (2 × 11 rounds) | −8.8%, −8.8% |
| `OpSelect` only (9 rounds) | +1.8% (noise) |
| `OpSetField` only (9 rounds) | +1.9% (noise) |
| `OpFieldRef` only (9 rounds) | −8.2% |
| no cache: depth 0 scan in `FieldRef.find` before the BFS (11 rounds) | −8.7% |
| same, micro probe (7 rounds) | −0.1% |

The gain is all in `FieldRef.find`, and it is not the name scan. `find`
built a `level` slice and a `hits` slice on every read and write
through a field ref (9.05M per run). A direct field is the common case
and needs neither: depth 0 has one struct, and its field names are
unique. Scanning `s.Def.Fields` first and falling back to the BFS only
for promoted fields keeps the semantics and drops the allocations.

The name scan in `structMember` and `setField` is cheap at these field
counts, which is consistent with step 4b. So the per-site cache was
dropped. It would have added a second site mechanism and a REPL
invalidation question for no measured gain.

Lesson: step 6's "~8–9%" figure was right in size but wrong in cause.
The by-name lookup count pointed at a cache. The real cost was the
allocations in one of the three paths, and splitting the bound per path
found it.

## Cumulative: the whole stack (#624 → #625 → #629)

One interleaved run of all four builds: main (`9f81e6cb`, the stack's
base), #624 (step 3), #625 (step 4a) and #629 (step 7). Outputs were
identical on every side.

| build | grafana-openapi (11 rounds) | vs previous | micro (7 rounds) | vs previous |
|---|---|---|---|---|
| main | 3.304s | | 0.830s | |
| #624 | 2.796s (−15.4%) | −15.4% | 0.579s (−30.2%) | −30.2% |
| #625 | 2.603s (−21.2%) | −6.9% | 0.604s (−27.2%) | +4.4% (noise) |
| #629 | 2.408s (−27.1%) | −7.5% | 0.577s (−30.5%) | −4.6% (noise) |

The micro probe has one interface conversion and few field refs, so
only step 3 moves it. The ±4% between its last three rows is noise.

## Re-measured after rebasing onto `ab53a3b2`

main moved under the stack: the oapi-codegen compatibility stack
(#638–#649) landed, touching VM stores, return coercions, reflect and
dispatch. The three layers rebased without conflicts (`gh stack rebase`), and
lint and `go test ./...` pass on each layer. This was re-measured with the
same setup as above: one interleaved run of all four builds, outputs
identical on every side.

| build | grafana-openapi (11 rounds) | vs previous | micro (7 rounds) | vs previous |
|---|---|---|---|---|
| main (`ab53a3b2`) | 3.394s | | 0.792s | |
| #624 | 2.815s (−17.1%) | −17.1% | 0.574s (−27.5%) | −27.5% |
| #625 | 2.532s (−25.4%) | −10.1% | 0.570s (−28.0%) | −0.7% |
| #629 | 2.357s (−30.6%) | −6.9% | 0.569s (−28.2%) | −0.2% |

Every step still pays on the new base. The cumulative gain on
grafana-openapi grew from −27.1% to −30.6% while main itself stayed at
~3.3–3.4s. The pairwise `compare.sh --full` (5 rounds, main vs each layer)
agrees: −17.0%, −25.1% and −31.1% on grafana-openapi, no regression, and
clickhouse-settings unchanged (0.022s on every side).

## How to re-run

The realworld `compare.sh` (podhmo/minigo-usecasefuzz) needed bash 4
(`declare -A`), which macOS's bash 3.2 lacks; podhmo/minigo-usecasefuzz#10
makes it bash-3.2 compatible. The experiments above replicate its steps by
hand:

1. Fetch the pinned grafana checkout at the `targets.tsv` SHA
   (`fa8d6e65`) with `git fetch --depth 1`.
2. Build `cmd/minigo` for each side from a scratch worktree.
3. Build the `realworld/prof` harness against each side with a
   generated `go.mod` `replace`.
4. Run `minigo run .` in `realworld/tasks/grafana-openapi` with
   `TARGET_DIR` set. Interleave sides for N rounds, take the median,
   and check that outputs are identical.
