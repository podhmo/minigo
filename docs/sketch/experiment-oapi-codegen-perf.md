# Experiment: where does oapi-codegen's wall time go?

Status: in progress. Branch `fix/oapi-codegen-regressions` carries the
two regression fixes found on the way (step 0).
`perf/sync-builtin-callbacks` is stacked on it and carries steps 4–13.

Steps 0–5 measured the program as shipped, goimports included. From
step 6 on, the baseline binds goimports natively. The index and an
interpreted goimports are workload facts, not interpreter tuning. Steps
6–13 ask what the interpreter itself can lose from there.

Question: oapi-codegen under minigo takes ~6s per `go:generate` line
(~50x native). The workload executes nearly the whole program
(kin-openapi, text/template, json/v2, goimports), so it shows raw
interpreter speed (see `docs/sketch/ja/experiment-oapi-codegen.md`).
Which levers move it? Are they the same ones as grafana-openapi's
(`docs/sketch/experiment-frame-path-perf.md`)?

This report is updated after every step. Each step records the method
so it can be re-run, the numbers, and the verdict.

## Verdict so far

**From the native-goimports baseline (steps 6–13):**
`petstore-expanded/strict` goes from ~1.95s to 1.04s (−47%), and the
output stays byte-identical. The single largest step is binding
`text/template/parse` (step 13, −33% on its own); the other eight
commits are small interpreter-level fixes. Three findings:

- The remaining cost is diffuse. No mutator function holds more than
  ~12% of samples. Loading and compiling take ~25% of wall, and Linux
  GC marking takes 44% of CPU samples, but on spare cores.
- A per-call `runtime.Stack` was still the largest single item (−5.7%
  once replaced by a g-pointer read).
- macOS CPU profiles of this workload are not trustworthy (step 7).
  Profile in a Linux container.

**For the program as shipped (steps 0–5):**

The levers differ from grafana-openapi's. There, the main-thread hot
paths were per-instruction work (global resolution, interface checks,
field refs). Here, two costs outside the interpreter loop dominate:

1. **goimports' module-cache index (machine-dependent, ~50%).**
   x/tools reads `~/Library/Caches/goimports/index-*` on every
   `imports.Process`, even when nothing is missing. With a 58 MB index
   it is 41% of instructions, and an empty cache dir halves wall time
   (7.0s → 3.6s). It is not minigo's to fix, but every benchmark has to
   pin it. The numbers below use the no-index setting.
2. **Goroutine identity per `VM.Call` (~17%).** `runtime.Stack`
   walks the whole deep interpreter stack. Steps 4 and 5 remove the
   Calls that need it and reach 80–90% of the upper bound:
   `petstore-expanded/strict` −13.5%, `only-models` −20.0%.

On the way: two regressions that had broken every oapi-codegen line
(step 0), and one SILENT bug (`sync.Pool.Put` ran a struct's niladic
methods, step 5).

All 53 lines stay byte-identical with native on top of steps 0–5. The
whole run (with the index, one run each) went from 275s to 257s. It
moves less than one line does, because the index parse is half of
each line there.

GC tuning is again not a wall-time lever (`GOGC=off` −2.5%).

On the TODO's `--src` packages: binding them would also be a speed
lever. text/template and json are ~37% of the no-index instructions.
But the largest instruction group (~50%) is goimports' go/printer,
go/token and go/scanner path, which no `--src` flag controls.

## Setup

- Target: oapi-codegen @ `43281d18a9d0`, the realworld
  `oapi-codegen-examples` pin, fetched into
  `~/.cache/minigo-realworld/oapi-codegen`. Runs use a scratch copy,
  because each run rewrites the generated files.
- Machine: 10-core arm64 macOS, Go 1.27.1.
- Not all 53 lines are needed for profiling. Six lines cover the
  shapes: `only-models` (models only), `client`, `anyof-allof-oneof`,
  `overlay/api`, `petstore-expanded/chi/api` (server) and
  `petstore-expanded/strict/api` (strict server, the largest output).
  `strict` is the main profiling subject. The full 53 lines run only
  as a correctness gate, with realworld's `task.sh` (`rc` plus sha256
  per written file, diffed against the native binary).
- Profiling harness: a copy of realworld's `prof` that chdirs into the
  example dir and runs `cmd/oapi-codegen` with the same `--src` modes
  and arguments as the CLI.
- Instruction counting: a temporary build (not committed) that counts
  executed instructions per chunk and sums them per package and per
  function.

## Step 0: main could not run oapi-codegen at all

The first run on main (`18411142`) failed every line. Two regressions
had landed since the 53/53 report (`ab53a3b2`). Both were found by
`git bisect run` over the `only-models` and `extensions/xenumnames`
lines.

### 0a. `cannot use Tag as Tag` (from #675)

#675 added a declared-key-type check to map index operands. The store
target `m[k] = v` goes through `OpIndexRef`, which unwrapped the key
before the store saw it. A key of `type Tag compact.Tag` is a
defined type over another named struct. Unwrapped, it is a bare struct
whose typedef is `compact.Tag`, and the check rejected it against
`Tag`. x/text's `cases` init does exactly this
(`internal.NewInheritanceMatcher`). Every oapi-codegen line imports it,
so every line failed.

Fix: `IndexRef.Key` keeps the materialized operand with its tag. The
map hash and int-index uses unwrap it. Pinned as
`mapkey_defined_over_named`, which also covers nested `mm[a][b] = v`,
`ms[a][i] = v`, `mp[a].f = v`, and named-int slice indexes through refs.

### 0b. `cannot use SortedMap as []E` (from #662)

#662 removed `tdShapeEq`'s "both spellable" arm as dead code, reasoning
that identical AST implies identical underlying types. The arm is not
dead: `TypSpelling` resolves type params through each side's binds, so
`[]E` with E bound to `KeyValue` matches `type SortedMap []KeyValue`.
Without the arm, a generic `S ~[]E` argument passed on as a `[]E`
parameter traps. `slices.SortStableFunc` does this from
`internal/fmtsort`, which text/template's `range` over a map reaches.
Three lines failed (`extensions/xenumnames`, `generate/serverurls`,
`webhook`). The other 50 were byte-identical with native.

Fix: restore the arm with a comment on why it is needed. Pinned as
`generic_namedslice_passon`.

Both pins fail on main and pass with the fix (negative control).
`make lint` and `make test` pass.

Lesson: the difffuzz regression suite did not catch either one. Both
need a shape that the generator does not produce: a defined type over
a named type from another declaration, and a generic constraint
passed through a second generic call. oapi-codegen is a good net for
these. A periodic 53-line run would have caught both on the day they
landed.

## Step 1: first profile (`petstore-expanded/strict/api`)

Wall time is ~6.9s (native 0.14s). `/usr/bin/time -l`: 11.5s user,
0.23s sys.

The CPU profile has a different shape from grafana-openapi's:

| node | CPU (of 10.6s samples) |
|---|---|
| `syscall.rawsyscalln` under `bufio.Scanner.Scan` → `os.File.Read` | 2.41s |
| GC (`gcDrain`, cum) | 3.67s |
| VM loop (cum) | 4.95s |

The `rawsyscalln` figure is misleading. The process's whole sys time
is 0.23s, so these samples are not CPU spent in the kernel. A `go`
wrapper on PATH shows the child processes (`go env` ×3) take 11 ms
each, so it is not waiting for them either. What the profile shows is
a host-bound `bufio.Scanner` driven by script code, line by line,
which leads to step 2.

## Step 2: instructions per package

The counting build on `strict` executed 150.6M instructions:

| package | share |
|---|---|
| `golang.org/x/tools/internal/modindex` | 41.7% |
| `text/template/parse` | 13.1% |
| `go/printer` | 10.8% |
| `go/token` | 9.8% |
| `encoding/json/internal/jsonwire` | 4.4% |
| `go/scanner` | 3.6% |
| `github.com/oasdiff/yaml3` | 2.9% |
| `encoding/json/jsontext` | 2.7% |
| `text/tabwriter` | 2.4% |
| `go/parser` | 1.8% |
| `compress/flate` | 1.6% |
| `encoding/json/v2` | 1.0% |
| kin-openapi + oapi-codegen's own `codegen` | 0.3% |

41% is one function: `modindex.readIndexFrom`. goimports
(`imports.Process`, called on every generated file) reads the module
cache index from `os.UserCacheDir()/goimports` *unconditionally*, even
when no import is missing (`getFixesWithSource` in
`x/tools/internal/imports/fix.go`). On this machine the index is a
58 MB file, because the module cache is large. Native parses it in
milliseconds. Under the interpreter it is ~62M instructions.

This cost depends on the machine, not on oapi-codegen. A CI runner
with an empty cache has no index and skips it. Measured by pointing
`HOME` at an empty directory (GOPATH, GOMODCACHE, GOENV and GOCACHE
pinned to their real values, so only the index lookup changes):

| | wall | instructions |
|---|---|---|
| with the 58 MB index | 7.0s | 150.6M |
| no index | 3.6s | 87.8M |

The output is byte-identical with native in both cases.

So on a developer machine, half of every oapi-codegen line is goimports
parsing an index that the line does not use. Benchmarks need to fix
this variable. The numbers below say which setting they use.

Without the index, the remaining 87.8M instructions are:

| group | share | bound or source |
|---|---|---|
| go/printer, go/token, go/scanner, go/parser, go/ast, text/tabwriter (goimports and `format.Source`) | ~50% | source: no binding |
| text/template/parse + text/template | ~23% | source, forced by `--src` |
| encoding/json (jsonwire, jsontext, v2, flags) | ~14.5% | source, forced by `--src` |
| yaml3 + yaml/v3 | ~5.5% | source |
| compress/flate | 2.7% | source |

## On the TODO "oapi-codegen needs `--src` for seven packages"

Would binding the missing members, so that the `--src` list goes away,
make it faster? By instruction share (no-index setting):

- text/template: ~23%, almost all of it in `parse` (the lexer). The
  templates are parsed on every run. A bound text/template that can
  call script funcs (FuncMap) would run parse and exec natively. This
  is the largest single gain the TODO could give, but also the hardest
  part. Exec calls back into script funcs, and walks script values
  through reflect.
- encoding/json: ~14.5%. A bound json that calls script
  `UnmarshalJSON`/`MarshalJSON` would run the scanner natively.
  kin-openapi drives decoding through its own `UnmarshalJSON`
  methods, so part of the work stays in the script.
- hex/base64/base32/slices/maps: ~0.3% combined. Binding them helps
  compatibility, not speed.

So yes, it is a performance lever too, worth up to ~35% of
instructions on top of its compatibility value. But the biggest group
is outside the TODO: goimports' go/printer/go/token/go/scanner path,
~50%, which no `--src` flag forces. Instructions are not time,
though: the step 3 measurements decide.

## Step 3: GC and the goroutine-id upper bound

Interleaved whole-process runs (`bench.sh`: 7 rounds, median, no
goimports index), `petstore-expanded/strict`:

| build / setting | wall | vs base |
|---|---|---|
| base (step 0 fixes) | 3.304s | |
| `GOGC=400` | 3.142s | −4.9% |
| `GOGC=off` | 3.222s | −2.5% |
| `GOMAXPROCS=1` | 4.779s | +44.7% |
| upper bound: `goroutineID()` → constant | 2.748s | −16.8% |

GC is not the wall-time lever, for the same reason as on
grafana-openapi: the mark work runs on idle cores. It matters only on
one core.

The upper bound is the surprise. The profile showed `vm.goroutineID`
at only 0.16s (~4.5%). `VM.callBounded` calls it on every `VM.Call`, to
tell a same-goroutine re-entry from a callback that fires on a foreign
goroutine. Go has no goroutine-id API, so it parses `runtime.Stack`.
Since Go 1.21 the traceback also prints the *outermost* 50 frames, so
it walks the whole stack, and the interpreter's Go stack is thousands
of frames deep. A counting build gives 10,677 Calls per `strict` run,
~50 µs each. As in step 3 of the grafana report, truncated pprof
stacks under-count a cost that sits on deep stacks.

The bound holds on other lines too (5 rounds): `only-models` 1.490s →
1.161s (−22.1%), `petstore-expanded/chi` 2.800s → 2.288s (−18.3%).
The output is byte-identical.

No cheap and correct goroutine identity exists. `runtime.NumGoroutine`
is documented as possibly inconsistent, and the `runtime.Stack` header
is the only public source. So the fix has to remove *Calls that need
the check*, not make the check cheaper.

Where the Calls come from (counting build, callee ← nearest caller):

| source | calls | share |
|---|---|---|
| intrinsic `sort.Search` (x/text `normLang`/`normRegion`/`tag.Index`/`getCoreIndex` closures, in package init) | 3,699 | 34.6% |
| intrinsic `strings.IndexFunc`/`LastIndexFunc` (json/v2 `parseFieldOptions`, `isLetterOrDigit`, `notIdentifier`) | 3,270 | 30.6% |
| `structDataHost` → json/v2 `Decoder.*`/`Encoder.*` niladic methods (9 + 5 methods, ~200/~90 each) | 2,213 | 20.7% |
| `scriptWriter.Write` ← `fmt.Fprint` | 407 | 3.8% |
| the rest (sync.Pool `New`, reflect-called template funcs, …) | ~1,100 | ~10% |

## Step 4: builtins that only call back synchronously (`7bec264c`)

The first two rows are intrinsics (`runtime.BuiltinFunc`). The VM calls
them on its own goroutine as `c.Fn(v, args)`, and they call the script
predicate back through `v.Call` before returning. Those Calls are
same-goroutine re-entries by construction. The VM just cannot tell,
because some builtins *do* keep the caller: `time.AfterFunc` and
`context.AfterFunc` fire later on a timer goroutine, and
`strings.FieldsFuncSeq` returns an iterator.

Design: an opt-in flag `BuiltinFunc.SyncCallbacks`. It declares that
Fn calls back only on the calling goroutine, before it returns, and
never retains the caller. For such a builtin the VM passes
`ownerCaller{v}`, whose `Call` enters the in-flight Call directly
(`callDepth++`, no id). If no Call is in flight, it falls back to the
general path. 16 builtins are marked after reading each body:
`strings.*Func` and `Map`, `sort.Search`/`SliceStable`/`SliceIsSorted`,
`slices.*Func` and `filepath.WalkDir`. `strings.FieldsFuncSeq` stays
unmarked. `procOf` learned the wrapper type.

Test: `builtin_sync_callbacks` covers a panic in a predicate recovered
by the script, nested SyncCallbacks builtins (`sort.SliceStable` whose
less calls `strings.IndexFunc`), predicates on 8 script goroutines, and
`strings.Map`/`TrimFunc`. `go test -race` passes.

Calls per `strict` run: 10,677 → 3,693.

| | strict (7 rounds) | only-models (5 rounds) |
|---|---|---|
| base | 3.315s | 1.514s |
| step 4 | 3.141s (−5.2%) | 1.276s (−15.7%) |
| upper bound | 2.739s (−17.4%) | 1.162s (−23.2%) |

Two-thirds of the Calls were gone, but only a third of the bound was
gained on `strict`. Cost per Call scales with stack depth, and the
init-time `sort.Search` calls run on shallow stacks. What remains is
step 5's: Calls from deep inside the json decoder.

## Step 5: `sync.Pool.Put` ran a Decoder's methods (`a51e8335`)

The third row was the strangest. jsontext's pools return a
`*Decoder`/`*Encoder` with `bufferedDecoderPool.Put(d)`. `sync.Pool`
is a bound host type, so `d` crosses a host `any` parameter, and
`toReflectValue` marshals a script struct for `any` into a scriptData
map. That projection exists for text/template's `evalField`, so
`structDataHost` *invokes every niladic exported method* and stores
the results as fields: `ReadToken`, `SkipValue`, `ReadValue`,
`UnreadBuffer`, … on every Put. Each call paid a deep-stack goroutine
id.

It is also a SILENT bug: putting a value with a side-effecting niladic
method into a pool runs the method.

```go
r := pool.Get().(*Reader)   // Next() advances r.pos
r.Next()                    // pos 1
pool.Put(r)
fmt.Println(r.Pos(), r.Next())  // go: "1 b"; minigo on main: "2 c"
```

oapi-codegen's output stayed byte-identical only because jsontext
resets a decoder before Put, so the extra reads failed harmlessly.

Fix: `sync.Pool.Put` passes a struct-shaped script value (a struct,
named or not, or a pointer to one) as is. Get hands it back unchanged
(`goValueOf` returns script values verbatim). Pinned as
`syncpool_put_methods`, which fails on main.

| | strict (7 rounds) | only-models (5 rounds) |
|---|---|---|
| base | 3.318s | 1.505s |
| step 4 | 3.133s (−5.6%) | 1.273s (−15.4%) |
| step 4 + 5 | 2.868s (−13.5%) | 1.204s (−20.0%) |
| upper bound | 2.746s (−17.2%) | 1.160s (−22.9%) |

Steps 4 and 5 reach ~80–90% of the bound, with no unsafe identity
trick. The remaining gap is the ~1,100 scattered Calls (Pool `New`,
`fmt` writers, template funcs called through reflect).

Open question, not pursued here: `structDataHost`'s eager method
invocation applies to *every* host `any` parameter, not only
text/template's. `sync.Map.Store`, `atomic.Value.Store` and
`context.WithValue` have the same shape as Pool.Put. A general rule
("containers keep script values opaque") would be better than a
per-method list.

## Step 6: a native-goimports baseline

goimports runs inside oapi-codegen, but its cost is a workload fact.
The index is a 58 MB cache file x/tools parses on every call, and
goimports' go/printer path is ~50% of instructions. Neither tells us
anything about the interpreter's own overhead. So the harness now binds
`golang.org/x/tools/imports.Process` natively, which also makes the
index irrelevant:

```go
e.Bind("golang.org/x/tools/imports", map[string]runtime.Value{
	"Process": &runtime.GoValue{V: imports.Process},
})
```

`petstore-expanded/strict` on that baseline, before any change below:

- ~1.95s wall, output identical to native.
- 42.6M instructions, ~35 ns per instruction over the execution part.
- Shares: text/template/parse 46%, json/v2 ~30%, yaml.v3 10%,
  compress/flate 5.5%.
- Load and compile take ~0.47s:
  - `loadPathFrom` 0.24s (78 packages, 518 files; parsing is 0.09s
    of that).
  - `compile.Func` 0.10s (17,674 functions).
  - `compile.InitFunc` 0.13s (52 packages).

What did not help, or helped little:

- **`orderSpecs` cache (`162242bb`)**: names that are neither funcs
  nor methods were re-resolved per spec. Caching the negative answer
  gives −4.0% (1.952s → 1.873s). Landed.
- **Frame batching** (cherry-pick of `052a37ef`): −1.9%. Not landed;
  too small for its complexity.
- **GC knobs**: `GOGC=200` −3%, `GOGC=400` −6%, `GOGC=off` ±0, and
  `GOMAXPROCS=1` +50%. Allocation is 1.7 GB per run. `prepFrame` is 16%
  of `alloc_space` and `runtime.Tag` 17% of objects. GC runs on the
  other cores, so it is not on the critical path.
- **Opcodes**:
  - `OpLocalType` follows every type expression and is a no-op outside
    generic functions (~2.6M executions, small).
  - `OpInstantiate` doubles as indexing and allocates a type-args slice
    per `a[i]` (0.72M executions, ~1%).

A micro benchmark puts interpreter call overhead in perspective
(minigo vs native, per iteration):

| | ns/iter |
|---|---|
| empty loop iteration | 151 |
| + one function call | +~390 |
| + one method call | +~690 |

oapi-codegen makes ~2.8M calls per run, so calls are the bulk of the
execution part.

## Step 7: macOS profiles lie; profile in a Linux container

The micro benchmark's CPU profile on macOS showed `runtime.kevent` at
54% and `VM.loop` at only 14%. With `GOMAXPROCS=1`, kevent rose to 80%.
Yet `/usr/bin/time` showed user ≈ real, and `GOGC=off` did not change
the speed. The kevent samples sit under `netpoll` ←
`startTheWorldWithSema` (GC start/stop), so SIGPROF is being
misattributed. macOS `sample` does no better: it does not unwind most
Go frames.

The fix is to cross-compile the harness and run it in Linux. The
harness is `CGO_ENABLED=0 GOOS=linux GOARCH=arm64`, the container is
`golang:1.27-alpine`, and the host module cache is mounted read-only at
the same path with `GOPROXY=off GOFLAGS=-mod=mod`. There, `VM.loop`
covers 92% of the micro profile.

`strict`, 6 rounds in-process (Linux, wall 1.86s per round):

| bucket | share of CPU samples |
|---|---|
| GC background marking (other cores) | 44% |
| load, parse, compile, bootstrap | ≥10% (≥19% of mutator) |
| `coerce` (iface checks, `coerceConcrete`, `zeroValue`) | 12% |
| `selectMember` (half is lazy `Package.MemberV` loading) | 9.6% |
| `prepFrame` | 5.8% |
| `goroutineID` | 3.3% |

`resolveFieldTypes` and `resolveTypeRef` look hot, but they are mostly
first-touch package loading reached through `Package.Member`. They are
cached per typedef: 879 calls for 259 distinct names.

## Step 8: interface memo misses; `sync.Pool.New` (`04a5468e`, `33831c70`)

`ifaceSatisfied` memoizes per (interface, dynamic typedef, pointer),
but only struct and named values had a key. A counting build showed
the misses per run:

| value | uncached checks |
|---|---|
| `*runtime.IfaceNil` | 27,229 |
| `GoValue(*minireflect.RType)` | 7,689 |
| `Cell{IfaceNil}` | 3,781 |
| `GoValue(*errors.errorString)` | 1,656 |

A host box's method set follows from its reflect type, and a nil
interface value's from its tag. The key gains both. Result: 1.883s →
1.842s (−2.2%).

The remaining goroutine-id Calls came almost entirely from
`sync.Pool.Get` → script `New` → `adaptFunc` → `deepHost` →
`structDataHost`. This is the `Put` bug of step 5, mirrored: each pool
miss marshaled the fresh value and ran its niladic methods. That is
SILENT; `testdata/difffuzz/syncpool_new_methods` printed `1 1 / 2 2`
instead of `1 0 / 1 1`. A script `New` set on a host `sync.Pool` (as a
literal field or by assignment) now returns struct-shaped results
opaque. On macOS this measured −0.7%, within noise.

## Step 9: goroutine identity without `runtime.Stack` (`ca15014c`)

What remained of the goroutine-id Calls were genuine synchronous
callbacks:

- text/template calling script methods through `minireflect.RValue.Call`
- `fmt.Fprint` writing into a script `io.Writer`

Marking each one proves nothing general. But `Call` only *compares*
ids, and every id it holds belongs to a live goroutine (the Call owner,
or a helper inside a blocking host call). So the runtime g address is a
sound identity. A four-line assembly `getg` reads it on arm64 and
amd64. Other architectures keep the `runtime.Stack` parser.
`TestGoroutineID` checks stability and distinctness, and it passes on
arm64 and on amd64 under Rosetta. CI runs amd64.

| | strict (7 rounds) |
|---|---|
| before | 1.806s |
| g pointer | 1.702s (−5.7%) |

Checking the fallback with `GOARCH=arm` turned up a pre-existing
failure: `minireflect` does not build on 32-bit targets. It is recorded
in TODO.md.

### Next candidates

- **`prepFrame` allocates a `Cell` per local** on every call, plus the
  frame and its locals slice. Tried twice already (frame-path step 1 and
  step 6 here), with weak results both times.
- Load and compile: see step 11.

## Step 10: untyped constants memoize their conversion (`5211ff88`)

A chunk constant is one shared `*UConst`. `OpConst` pushes the pointer
itself. Every `r == ' '` re-ran the conversion
`adaptConst` → `constToBasic` → `fitsIntConst` (go/constant), and
against a named operand it also allocated a fresh `Named` through
`runtime.Tag`.

`UConst` now carries a single-entry `atomic.Pointer` memo. It is keyed
by what decides the conversion: the operand's typedef for named
operands, or the scalar kind for bare `int64`/`float64`/`string`/`bool`
operands. Only immutable results are stored. A miss (a constant used
against alternating types) re-converts as before.
`testdata/difffuzz/uconst_memo_types` alternates one site over seven
operand types, including across goroutines, and passes under `-race`.

| | before | after |
|---|---|---|
| call micro benchmark | 540 ns/iter | 480 ns/iter (−11%) |
| strict (9 rounds) | 1.712s | 1.684s (−1.6%) |

The micro benchmark compares a rune against four constants per call,
so it gains far more than oapi-codegen. On `strict`'s Linux profile the
conversion functions disappear; only `constPayload` is left, at 0.3%.
Constants were a hot spot of the lexer-shaped micro benchmark, not of
the workload.

## Step 11: load and compile (`804faabf`, `93a60de6`, `bbc11fc9`)

The Linux profile split the per-round load cost (6 rounds, CPU
seconds):

- `go/parser` ~0.58s
- `syntax.CheckLang` ~0.49s
- `compile.orderSpecs` ~0.45s
- `compile.Func` ~0.46s

The other items were smaller.

- **`CheckLang`** (`804faabf`): 3/4 of the checker's time went into
  `seen[n] = true` for every node. `ast.Inspect` never revisits a node,
  so only the subtrees the type-grammar walk gated need the mark.
  `localDecls`, a second full walk, is now built on the first
  shadowing query. A file whose lang is at or past the newest gate
  (`latestGate`, kept current by `TestLatestGate`) is skipped outright.
  Result: 1.690s → 1.626s (−3.8%).
- **`orderSpecs`** (`93a60de6`): `funcRefs` unions each function's
  referenced names transitively into its callers'. Those sets held every
  identifier, including locals, fields and builtins, which all fell out
  at the end anyway. `refs` now keeps only package vars/consts, funcs
  and method names, and method names resolve through a map built once.
  Result: 1.626s → 1.593s (−2.0%).
- **Concurrent parsing** (`bbc11fc9`): a package's files are read
  first, then their `Pos` range is reserved in the shared `FileSet`
  with a placeholder. Each file parses into a private `FileSet` whose
  next base a filler file moves to the reserved slot. The `token.File`s
  join the shared set in order via `AddExistingFiles` (Go 1.25+).
  Bases, line tables and positions equal the sequential parse's
  (`TestParsePackageFilesBases`). Result: 1.600s → 1.569s (−2.0%).

CPU on the same 6-round Linux profile, before → after:

| | before | after |
|---|---|---|
| `syntax.CheckLang` | 0.49s | 0.08s |
| `compile.orderSpecs` | 0.45s | 0.19s |
| `indexFiles` (includes `CheckLang`) | 0.56s | 0.13s |
| `go/parser` | 0.58s | 0.62s, now off the critical path |

What remains is `compile.Func` (~0.5–0.6s per 6 rounds; ~17k
functions at ~5µs each). Its cost is spread thin over name-lookup maps
and emit, with no single hot spot. The resolver's `Locate` is ~0.12s.

## Step 12: text/template (investigation)

On the current baseline, `text/template/parse` is 46.4% of executed
instructions (19.8M). `text/template` itself (exec) is 3.1%. Within
parse, the lexer (`lexer.*`, `lex*`, `isSpace`, …) accounts for at
least 12.4M and `Tree.*` for at least 2.9M.

What oapi-codegen does on every line:

- `LoadTemplates` parses all embedded templates, 48 files and ~200 KB.
- `buildServerTemplates` clones the tree once per framework (8) and
  parses each framework's `hooks.tmpl` into its clone.
- Only the handful of templates the config asks for are executed.

A stand-alone script repeating exactly that (64 templates after
`define`s, 8 clone+hooks):

| | parse | clone + hooks |
|---|---|---|
| native | 0.010s | 0.000s |
| minigo, `--src text/template` | 0.534s | 0.017s |

So template parsing is ~0.55s of `strict`'s 1.57s, about a third of
the wall time. Exec is small.

**Can the interpreter parse lazily?** Not without changing behavior:

- `Parse` returns syntax errors at the call, and oapi-codegen fails on
  them there.
- `{{define}}` registers names that only a full parse reveals, and the
  hooks override them by parse order.
- Nothing lets the interpreter prove a tree is never read; trees sit
  in `t.common`'s map until exec looks them up by name.

A "scan defines now, parse bodies later" scheme is a change to
text/template, not to the interpreter. It also moves error reporting
for broken user templates. The lever is to make parsing run natively.

Options:

1. **Bound text/template, parse and exec native.**
   - Today the bound package has no `FuncMap` (`template.FuncMap` is
     undefined), so script funcs cannot be registered.
   - Exec would walk script data through the `scriptData` projection,
     which runs every niladic method eagerly (the SILENT hazard of
     steps 5 and 8) and deep-copies the data on each `Execute`.
   - Exec is only 3% of instructions, so this option adds the most
     semantic risk for almost no extra speed.
2. **Source text/template, native lexer.**
   - Override `lex` and `(*lexer).nextItem` with a vendored copy of the
     host lexer that returns the source package's `item` structs.
   - This needs a new "native override for a source function"
     facility, which does not exist. It also needs a version check
     that the GOROOT `lex.go` matches the vendored copy, since
     `itemType` numbering is positional.
   - It removes at most the lexer's ~2/3 of parse.
3. **Source text/template over a bound `text/template/parse`.**
   - Trees become host values. Exec reaches parse through ~29 names:
     - about 20 `*XxxNode` types
     - `Node`, `Tree`, `Parse`, `IsEmptyTree`
     - `NodeType` and two of its constants
   - Type switches on host pointers already work: `typeMatches`
     compares the box's reflect type with the typedef's `HostNew`.
     Field reads go through `hostField` (reflection), and interfaces
     are hand-written `KindInterface` typedefs, as for `io.Writer`.
   - `parse.Parse` receives script func maps, but it only checks
     names, so a wrapper can pass name sets.
   - Removes nearly all of the ~0.55s. Exec pays reflection per field
     read, but exec is small.

Option 3 has the best gain-to-risk ratio. Its unknowns are host nil
pointers inside trees (`t.Tree == nil`, `Root == nil`), and ranging
over host slices of interface elements (`[]parse.Node`). A spike
binding just enough of `parse` for the stand-alone script would settle
both.

## Step 13: bound `text/template/parse` (`604ce3bd`)

Option 3 from step 12. The binding covers:

- 21 node types plus `Tree`, as `hostType`s whose `HostNew` returns
  `new(T)`
- `Node`, as a `KindInterface` typedef
- `NodeType`, `Pos` and `Mode`, as `HostScalar` named basics
- the 21 `NodeXxx` constants and `ParseComments`/`SkipFuncCheck`
- `New`, `NewIdentifier`, `IsEmptyTree` and `Parse`

`Parse` gets the script's `FuncMap`s but the host parser only checks
names, so `funcNameSets` hands it name sets. Script func values never
cross.

The spike hit exactly one gap. `cmd.Args` (`[]parse.Node`) unboxed to a
slice tagged `[]Node`. `goValueOf` spells a named element type without
its package, so the slice failed to bind `evalFieldNode`'s
`args []parse.Node`. A host slice whose element type belongs to a host
package now unboxes untagged and adopts the slot's type. The two
unknowns from step 12 did not bite: nil host pointers already read as
`nil`, and host slices of interface elements unbox element-wise.

| | before | after |
|---|---|---|
| stand-alone template parse (64 templates) | 0.534s | 0.021s |
| strict (9 rounds) | 1.552s | 1.038s (−33%) |
| all 53 lines vs native (`noidx.sh`, CLI) | identical | identical (46.6s total) |

`testdata/difffuzz/template_src_hostparse` (`SRC=text/template`)
compares an interpreted text/template against native. It covers
define/template/block, range with else/break/continue, with, variables,
builtins, methods with args, number and char literals, trim markers,
Clone with a block override, and four error messages.

Writing it surfaced three pre-existing gaps, now in TODO.md:

- `{{(index .Items 1).Name}}` panics in exec (`MethodByName` on a zero
  value), with or without the binding.
- `strconv.UnquoteChar` is unbound. The parse-from-source path trapped
  on `{{'a'}}`.
- html/template from source does not get past `bytes.IndexAny` and
  `bytealg`. Its escaper is the one place that builds and mutates parse
  trees, so tree mutation on host nodes remains untested.

## Side check: grafana-openapi

The branch's general changes (load, goroutine id, interface and constant
memos) also run under realworld's `grafana-openapi` (grafana at
`fa8d6e65`, `minigo run .`, CLI). main `18411142` against `1faea9e7`,
9 interleaved rounds on macOS. Output is identical to `go run` on both.

| | main | branch |
|---|---|---|
| median | 2.322s | 2.224s (−4.2%) |
| range | 2.269–2.337s | 2.186–2.314s |

The `text/template/parse` binding plays no part here; the gain is the
smaller, general set.

## Review follow-up (`9f5efbc9`)

A review of #699 found five divergences in the bound
`text/template/parse` that main (parsing from source) did not have:
constants that did not compare or compute as named ints, element
stores into tree slices that never reached the host tree, `parse.New`
rejecting its func maps, `IsEmptyTree(nil)` trapping, and a typed-nil
func-map entry read as undefined. The constants now follow
`reflect.Kind`'s boxed-host pattern; the constant-comparison fix also
corrects `reflect.Kind` comparisons such as `k == 2`, which were false
on main. A slice read from a host field keeps the host slice and writes
element stores through to it. `template_parse_hostapi` pins all five.
Strict stays at 1.06s (9 rounds, 1.069s before); all 53 examples remain
identical.

## Step 14: compile.Func (investigation, nothing landed)

A Linux profile after the review follow-up (6 in-process rounds of
strict, 12.03s CPU): GC background marking 44%, `VM.loop` 38%,
`compile.Func` 5.7% (0.69s, ~115ms per run), `go/parser.ParseFile`
5.1%, `orderSpecs` 2.0%, `InitFunc` 2.2%, `indexFiles` 1%, `CheckLang`
and `Locate` 0.6% each. `text/template/parse` is gone (0.08%).

Inside `compile.Func` the cost is flat: statement and expression
compilation, map lookups for names, and slice growth for code and
constants, with no node above 0.13s cumulative. One suspicious shape
was each operator of a left-deep binary chain re-scanning its whole
subtree (`hoistedArgCalls`, `pureOperand`, `constValue`), which is
O(depth²). Memoizing the three per node did not move strict (1.051s
→ 1.051s, 9 rounds). Real chains are short; only a synthetic
1600-operand concatenation gained (0.32s → 0.25s), so the patch was
not landed.

GC sets the ceiling for allocation work. GOGC changes the strict wall
time (7 rounds) as follows:

| GOGC | median |
|---|---|
| 100 | 1.070s |
| 200 | 1.000s |
| 400 | 0.979s |
| off | 1.024s |

At most ~8% of wall time is GC; marking runs on spare cores. The wall
time is mostly the mutator, and the interpreter loop is the largest
part. The next candidates are therefore in execution, not in load or
compile.

## Step 15: generic instantiations compiled per call (`96502314`)

In the allocation profile, `compile.(*compiler).emit` accounted for 10%
of all bytes (about 85MB per run). At 24 bytes per instruction, that is
over a million instructions per run. Counting `compile.Func` calls in
one strict run:

| | calls | decls | instructions |
|---|---|---|---|
| before | 17,558 | 1,174 | 1,128,808 |
| after | 2,923 | | 211,227 |

Before, almost all duplicates were generics. Every call of an inferred
generic mints a fresh `*runtime.Function` (`inferBinds`,
`instantiateFunc`), and `EnsureCompiled` compiled each one again:
`compress/flate.loadLE64` 2,696 times, `cmp.isNaN` 3,387, `cmp.Compare`
1,515, `slices.pdqsortOrdered` 702.

The chunk depends on the decl, file, name and binds. Bind typedefs are
embedded as constants, and their kinds steer the compile. The package
now caches the chunk under a key built from those parts, the same
sharing `WithBinds` copies already do. A bind is keyed by its pointer,
with one exception. Call-site inference mints a fresh `string`
typedef for `isNaN`'s T on every call, so a bare predeclared basic
typedef is keyed by its name. Its `OuterSpell` is ignored, because that
only spells function-local names inside a typedef's AST. Each entry
keeps its binds alive, so the addresses in a key cannot be reused. At
most 64 instantiations are kept per decl.

The remaining compiles come from binds rebuilt per call: an anonymous
`[]string` or a re-specialized named slice for `slices.Sort` (702), and
fresh pointer typedefs for `componentNames` (423). Identity-equal
keying of those needs a structural key, which risks sharing a chunk
whose embedded typedef spells differently.

Measurements:

- Strict: 1.080s to 1.001s (9 rounds, −7.3%; an earlier pair gave
  1.034s to 0.955s).
- All 53 examples: identical to native, 44.6s (46.2s before).
- grafana-openapi: identical output.

`generic_inst_chunk_share` checks that shared chunks keep each
instantiation's types, and `TestInstKey` covers the key.

### Follow-up: structural keys (`b656e5b6`)

Most remaining misses were fresh copies of one type. A `stringSlice`
had the same Pkg, File, Spec, Anon and Elem on every call, and
`Compare`'s `[]int` came from the same type expression. Those copies
come from call-site inference and re-specialization. `componentNames`
is different: its binds are genuinely different types from different
call sites.

The key now spells a typedef by what determines it:

- its package, file, spec and type-expression nodes
- its name and kind
- its local-type scope and identity counters
- recursively, its binds, element and display context (`OuterSpell`)

Fields, methods and embeds derive from those. Host-backed typedefs
still key by pointer.

| | compiles | instructions | strict (9 rounds) |
|---|---|---|---|
| pointer keys | 2,923 | 211,227 | 0.965s |
| structural keys | 1,316 | 150,662 | 0.962s |

That is close to the 1,174 decls, but the wall-time change is within
noise. The remaining copies were small functions (`Sort`, `Copy`,
`Compare`). All 53 examples stay identical, at 44.3s; grafana-openapi
does too.

## How to re-run

Scripts used (kept outside the repo; reconstructable from this
description):

- `run1.sh BIN DIR ARGS…`: one `go:generate` line in a scratch copy
  of `examples/`, wall time via `perl Time::HiRes`.
- `noidx.sh CMD…`: run with `HOME` pointed at an empty dir, and
  `GOPATH`/`GOMODCACHE`/`GOENV`/`GOCACHE` pinned to their real values
  (computed *before* `HOME` changes; `GOENV` contains a space on
  macOS, so quote it). goimports then finds no index.
- `bench.sh ROUNDS DIR "ARGS" label=cmd…`: interleaved rounds under
  `noidx.sh`, median per label. A label's cmd may carry `VAR=val`
  prefixes (`GOGC=off`).
- Profiling and counting harness: realworld's `prof/main.go` plus
  `os.Chdir(-C dir)`, `WithArgs`, and `WithPackageModes` for the seven
  `--src` packages. Build it against a checkout with a generated
  `go.mod` `replace`. The counting build adds an `atomic.Int64` to
  `bytecode.Chunk`, incremented in `VM.loop`, plus a callee/caller
  histogram in `callBounded`.
- Native-goimports harness (steps 6–9): the profiling harness with
  the `imports.Process` bind above. `hbench.sh ROUNDS DIR "ARGS"
  label=bin…` runs whole-process rounds interleaved, takes the median,
  and checks `err=<nil>`. Compare the generated file with the native
  output by `cmp`.
- Linux profiles (step 7): `dk.sh WORKDIR CMD…` wraps `docker run
  golang:1.27-alpine` with the scratch dir and the read-only module
  cache mounted.
- Correctness gate: realworld's `task.sh native` and `task.sh minigo
  BIN` with `TARGET_DIR` set, then `diff` (rc plus sha256 per written
  file).
