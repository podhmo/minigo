# Experiment: where does oapi-codegen's wall time go?

Status: in progress. Branch `fix/oapi-codegen-regressions` carries the
two regression fixes found on the way (step 0).
`perf/sync-builtin-callbacks` is stacked on it and carries steps 4–9.

Steps 0–5 measured the program as shipped, goimports included. From
step 6 on, the baseline binds goimports natively. The index and an
interpreted goimports are workload facts, not interpreter tuning. Steps
6–9 ask what the interpreter itself can lose from there.

Question: oapi-codegen under minigo takes ~6s per `go:generate` line
(~50x native). The workload executes nearly the whole program
(kin-openapi, text/template, json/v2, goimports), so it shows raw
interpreter speed (see `docs/sketch/ja/experiment-oapi-codegen.md`).
Which levers move it? Are they the same ones as grafana-openapi's
(`docs/sketch/experiment-frame-path-perf.md`)?

This report is updated after every step. Each step records the method
so it can be re-run, the numbers, and the verdict.

## Verdict so far

**From the native-goimports baseline (steps 6–9):**
`petstore-expanded/strict` goes from ~1.95s to 1.70s (−13%) with four
small commits, and the output stays byte-identical. Three findings:

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

- **Untyped constants are converted on every execution.**
  `adaptConst` → `constToBasic` → `runtime.Tag` runs for `r == ' '`
  each time and allocates a `Named`. In the call micro benchmark it is
  16% of samples. A per-constant single-entry cache of
  (typedef → value) would remove both the conversion and the
  allocation. It needs care: `Named` values must be immutable, and
  cache writes need atomics.
- **`prepFrame` allocates a `Cell` per local** on every call, plus the
  frame and its locals slice.
- **Load and compile (~0.47s, ~25% of wall)** is untouched.

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
