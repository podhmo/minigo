# Experiment: can persistent template trees beat parsing?

Status: closed; **do not adopt the persistent cache**. Date: 2026-10-09.

This follows [the oapi-codegen performance experiment](./experiment-oapi-codegen-perf.md).
That experiment ended with an interpreted template parser over a host lexer.
This one asks whether repeated CLI invocations can skip parsing by saving
its result and reconstructing fresh script trees on the next invocation.

The prototype lives in `/private/tmp/oapi-template-cache-experiment`. The
minigo VM and installed Go toolchain were not modified. No production
cache or feature flag was introduced.

## Verdict

**It works for the tested workload, but is slower.** All 53 generation
lines match the pinned native oracle byte for byte, including after the
slice-capacity correction. The final warm cache makes `strict` 21% slower
on the native-goimports baseline and 13% slower with interpreted
goimports and an empty index. The full 53-line warm run is 30% slower.

| final strict condition | cache disabled | cold cache | warm cache |
|---|---:|---:|---:|
| native goimports | 1.1624 s | 1.7291 s | 1.4069 s |
| interpreted goimports, empty index | 2.0895 s | 2.6645 s | 2.3539 s |

These are nine-round medians of whole-process executions. Conditions
rotate order each round. Every run exits successfully and reports
`err=<nil>`. Cold adds 49% and 28%, respectively; warm adds 21% and 13%.

The preceding 52 ms tree-copy probe was not a persistence benchmark. It
started with trees already present as script values and omitted the
neutral representation and its reconstruction. A compact disk format
still pays more to reconstruct the trees than the current parser pays to
build them using its host lexer.

## Plan recorded before implementation

1. Restrict the prototype to oapi-codegen's embedded base templates.
   Leave user-template parsing and framework hooks on the ordinary path.
2. Save successful parse results in a versioned, pointer-free format.
   Restore new script trees for each parse call. Never retain a previous
   Engine's values, typedefs, FuncMap closures or mutable host trees.
3. Test no cache, an empty cache and a populated cache. Measure full
   process wall time as well as individual cache stages.
4. Compare focused semantic probes with native Go, then representative
   examples and all 53 generation lines. Check errors and aliasing, not
   only generated output.
5. Adopt only if a warm-cache improvement survives key computation,
   loading, decoding, reconstruction and package-loading costs.

The plan was saved in the artifact directory's `report.md` before the
implementation. That file also records decisions as they occurred.

## Setup

- minigo: `perf/sync-builtin-callbacks`, commit
  `9b0c47daca5548c85795901bc0476efa9c3fde8a`.
- oapi-codegen: `43281d18a9d0d4adc921a2e697147aaf63fd6479`.
  The scratch base's `pkg/codegen/codegen.go` matches that checkout.
- Toolchain: Go 1.27.1, darwin/arm64, on the same machine as the preceding
  experiment. CPU-profile attribution is not used for this experiment.
- Target: a new scratch copy of `/private/tmp/oapiperf/src`, with only
  embedded base-template parsing routed through the experimental wrapper.
- Templates: 40 base files, 199,225 bytes, producing 64 associated
  templates. Eight framework hook files bring the embedded inventory to
  48 files; they remain on the ordinary parser path.
- Main subject: `petstore-expanded/strict/api`, using
  `--config=server.cfg.yaml ../../petstore-expanded.yaml`.
- Interpreter harness: current minigo checkout, seven source package
  modes from the preceding experiment, with native goimports enabled by
  default. Each invocation creates a fresh Engine.
- Ordinary-goimports comparison: disable that binding and override
  `os.UserCacheDir` in the harness to an empty directory. This fixes the
  machine-dependent goimports index without changing HOME, GOPATH or the
  module cache.

The cache-disabled baseline uses the same experimental source files with
the wrapper disabled. This controls helper-loading changes, rather than
charging them only to the cache condition. It is not an unmodified-GOROOT
baseline; any cost of merely installing the helpers is excluded from the
reported cache penalty.

## Step 0: repeat the standalone probe

Five separate runs, median:

| operation | time |
|---|---:|
| native template parsing | about 2 ms |
| current minigo template parsing | 214 ms |
| current minigo clone plus hook parsing | 7 ms |
| copy script trees and install them in a new namespace | 52 ms |

The copy probe calls `Tree.Copy` on each of the 64 associated templates,
installs the copies with `AddParseTree`, and checks each root's `String`
against the original. It suggested a possible margin of approximately
160 ms, but did not include a disk representation, decoding or full
generator execution. Tree.String equality alone is not semantic proof.

## Step 1: define the snapshot boundary

The public node fields are insufficient for faithful restoration.
`Tree.ErrorContext` needs the original text and each node's owning Tree.
Those fields are private. Constructing only exported node fields would
lose error-location information and would not restore the original graph.

The scratch GOROOT adds two experimental helpers to `text/template/parse`:
`ExperimentSnapshot` and `ExperimentRestore`. They execute as source
under minigo. A separate helper in `text/template`,
`ExperimentParseCached`, can access the current template's function map,
delimiters and namespace installation logic. No host parse tree crosses
into script code.

The snapshot records Tree identities, node identities and edges, source
text, names, delimiters, Mode, node positions, scalar values and numeric
flags. Reconstruction creates script structs with the receiving
package's types and restores owning-tree and node references. Functions
are not saved: execution uses the current Template's FuncMap.

The key includes codec/parser version, template name, text, delimiters
and the sorted effective function-name set. The wrapper uses the ordinary
Template.Parse mode, so Mode is fixed at zero at this boundary. The
version includes the pinned parser-source digest and minigo revision;
the experiment is fixed to this toolchain and architecture.

Each successful parse result is written to a same-directory temporary
file and published with rename. A checksum protects the body from
accidental corruption. Failed parses are not cached. Missing, corrupt or
unwritable cache files fall back to parsing. Different Parse calls receive
independent mutable graphs; ordinary Template.Clone sharing is retained.

## Step 2: JSON serialization overwhelmed the benefit

JSON was the first format because it was easy to inspect. The first
cold-cache strict run succeeded, but took **106.062 s**:

| traced stage, sum over 40 files | time |
|---|---:|
| normal parsing | 0.209 s |
| snapshot encoding | 104.592 s |
| writing | 0.071 s |

`union.tmpl` alone took 17.63 s to encode. The interpreted, reflection-based
JSON path was several orders of magnitude more expensive than the
work it was intended to remove.

The first warm run did not complete. It found two independent existing
minigo gaps, subsequently reproduced without the experimental GOROOT:

1. `strconv.IntSize` is missing from the bound package. Source base64
   decoding reaches it through JSON's byte-slice decoding.
2. After a harness-only IntSize workaround, JSON-decoded byte slices fail
   when assigned to another declared `[]byte` field:
   `cannot use slice as []byte`.

Neither was fixed in minigo here. The diagnostic workaround is confined
to `bug-harness`; it is absent from the final performance harness. The
failed warm attempt is not treated as a valid timing sample.

### Minimum reproductions

The first case is independent of JSON:

```go
package main

import (
    "fmt"
    "strconv"
)

func main() { fmt.Println(strconv.IntSize) }
```

Native prints `64` on this machine; minigo traps `undefined: strconv.IntSize`.

The second case, with only IntSize supplied through the diagnostic harness:

```go
package main

import (
    "encoding/json"
    "fmt"
)

type state struct { Text []byte }

func main() {
    var decoded state
    if err := json.Unmarshal([]byte(`{"Text":"YQ=="}`), &decoded); err != nil {
        panic(err)
    }
    assigned := state{Text: decoded.Text}
    fmt.Println(string(assigned.Text))
}
```

Native prints `a`; minigo traps at the struct initializer. The reproduction
uses the original installed GOROOT and the same source package modes as
oapi-codegen, establishing that the snapshot helpers are not required.

## Step 3: use a compact, length-prefixed format

Replace JSON with a format containing only fields relevant to each node
kind. Strings and bytes are length-prefixed, preserving arbitrary bytes.
Scalar conversions use bound strconv functions; the rest of graph
construction and restoration remains interpreted.

Wire v2 made both cold and warm strict execution practical again.
However, nine interleaved rounds still showed a regression:

| wire v2 strict condition | cache disabled | cold | warm |
|---|---:|---:|---:|
| native goimports | 1.1589 s | 1.6816 s | 1.3753 s |
| interpreted goimports, empty index | 2.1096 s | 2.6314 s | 2.3006 s |

The first 53-line gate passed after the oracle-version correction below.
That was not the end of semantic validation.

## Step 4: correct the oracle version and slice capacities

### Generated version comments

The first gate case differed only in the generated version comment.
Native, built in the scratch checkout, reported `(devel)`; minigo's
module metadata reported `v2.0.0-00010101000000-000000000000`. The
cache-disabled and cached interpreter outputs agreed.

Rebuild native with oapi-codegen's supported
`main.noVCSVersionOverride` linker setting using that exact version.
Do not strip comments or normalize files. Subsequent gates compare
complete file bytes with this explicitly pinned native oracle.

### Capacity is observable

An additional probe found a codec defect: a freshly parsed list with
length/capacity `3/4` was restored as `3/3`. The earlier append probe
shortened the slice before appending, so it only tested aliasing within
the existing length. It missed an append at the original length.

Wire v3 preserves capacities of node, argument, declaration, command,
identifier, field-name and text-byte slices. Restoration uses those
capacities. The cache version changes again. Retain v2's artifacts and
repeat the final measurements and correctness gate for v3.

This illustrates why output equality and even a few aliasing probes do
not establish the safety of a mutable-tree cache.

## Step 5: final validation and measurements

The final nine-round strict medians are in the Verdict table. A separate
traced cold run spent 0.182 s parsing, 0.492 s encoding and 0.038 s
writing; its cache-wrapper total was 0.729 s. A separate traced warm run
spent approximately 0.48 s decoding/restoring, about
0.015 s reading, and about 0.515 s in the cache wrapper overall. It read
801,952 bytes including checksums. Tracing perturbs timing; these stage
figures explain the cost and are not substituted for benchmark medians.

The disk is not the dominant expense. Decoding constructs a neutral
record graph, then reconstructs the script-node graph. That is more work
than the host-lexer-assisted parser's direct construction. The earlier
Copy probe avoided the neutral graph entirely.

### Correctness

- Native tests for modified `text/template` and `text/template/parse`,
  including the snapshot tests, pass under `go test -race`.
- `go vet` passes for those packages and the interpreter harness.
- Differential probes agree across native and minigo, cache disabled,
  cold, warm and native-produced snapshots restored by minigo.
- Probes cover current FuncMap selection, undefined functions, syntax
  errors, numeric values and overflow errors, execution-error locations,
  define order, empty redefinitions, independent Parse graphs, Clone
  sharing, slice capacity, shortened-slice append and stale aliases after
  field rebinding.
- Corrupt-cache and unavailable-cache fallbacks agree with native.
  Native concurrent-writer tests also pass under the race detector.
- All six representative examples run successfully.
- **All 53 generation lines succeed and every written file's full
  SHA-256 matches native**, both with the cache disabled and warm.

The minigo repository-wide test suite was not rerun: its implementation
was unchanged. These checks validate the scratch prototype, not a merged
runtime feature or arbitrary user-mutated parse graphs.

### Full workload

| final 53-line condition | serial process wall total |
|---|---:|
| native | 5.968 s |
| cache disabled | 44.529 s |
| warm cache | 57.704 s |

These totals are one serial run per line and condition, not repeated
benchmark medians. Warm adds 13.175 s, approximately 29.6%. The cache
also fails its intended cross-process amortization case.

## Retrospective

### What this experiment established

A persistent parse-result cache can preserve the observed behavior of
this workload without mutable host trees. But building a serialization
representation and reconstructing script values erases the apparent
benefit of skipping parsing. The straightforward persistent-cache
implementation is not a performance candidate.

The useful first measurement was not the cache hit rate. It was the
cost of materialization from a neutral representation. The 52 ms Copy
probe measured an easier operation and gave an overly optimistic budget.

### What it did not establish

- This does not rule out a codec that directly materializes nodes
  without first allocating a neutral record graph; that remains unmeasured.
- It does not measure sharing parsed script trees inside one process.
  That avoids serialization and should be treated as a separate experiment.
- It does not measure lazy parsing or restoring only trees actually
  observed. The prototype eagerly restores every saved tree.
- It does not serialize arbitrary user-mutated ASTs with shared slice
  backing arrays, or replace arbitrary direct calls to Tree.Parse.
- It does not establish safety across other Go versions or architectures.

### Next experiment

Do not add this cache to normal minigo execution. If continuing, first
measure several generator jobs in one process using independent Engines,
with an explicitly designed immutable representation shared between jobs.
Separate package-loading reuse from template reuse, and define how
initializers and global state are reset before attempting Engine reuse.
The existing script-tree Copy result is a useful budget for that question,
but is not evidence that a process-sharing design is already safe or fast.

## How to re-run

The complete reproducible sources and recorded results are checked in at
[`experiments/oapi-template-cache`](../../experiments/oapi-template-cache/README.md).
The original scratch directory was `/private/tmp/oapi-template-cache-experiment`;
rerunning no longer depends on that directory. See the experiment README for
pinned toolchain/target requirements and optional input paths.

From `experiments/oapi-template-cache`:

```sh
python3 make_wire.py --work /tmp/oapi-template-cache-repro
python3 build.py --work /tmp/oapi-template-cache-repro --validate
python3 check_semantics.py --work /tmp/oapi-template-cache-repro
python3 run.py representatives --work /tmp/oapi-template-cache-repro
python3 run.py bench --work /tmp/oapi-template-cache-repro
python3 run.py gate --work /tmp/oapi-template-cache-repro
python3 probes.py templates --work /tmp/oapi-template-cache-repro
python3 probes.py bugs --work /tmp/oapi-template-cache-repro
```

Run performance measurements sequentially. `bench` rotates condition
order and recreates each cold cache before using it. The runner rejects
both nonzero exit status and interpreter errors. `gate` uses the checked-in
53-line inventory and output paths, computing fresh complete SHA-256 hashes.
New outputs stay in the specified workspace. `results/` preserves the actual
experiment timings, manifests, traces and failures; `archive/` preserves the
original drivers, generated helper sources and chronological plan/report.
Copied dependencies, binaries and cache directories are not checked in.

`OAPI_TREE_CACHE` opts into the scratch wrapper. An unset value selects
ordinary Parse. `OAPI_TREE_TRACE=1` emits per-template stage timings to
stderr. These are experiment controls, not minigo CLI feature flags.
