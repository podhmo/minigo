# Experiment: persistent template parse-tree reuse

Status: closed; persistent cache rejected after final correctness and performance checks.
Date: 2026-10-09 (Asia/Tokyo).

## Hypothesis and scope, recorded before implementation

The current minigo branch is `perf/sync-builtin-callbacks`, revision
`9b0c47daca5548c85795901bc0476efa9c3fde8a`. Re-parsing about 199 KB of
oapi-codegen embedded templates takes approximately 214 ms in a standalone
probe; copying and installing its 64 resulting trees takes approximately
52 ms. These are preliminary five-run medians, not full-generator results.

Implement an opt-in cache in a scratch oapi-codegen checkout. Cache only
embedded base template parsing; leave user templates and framework hooks
on the normal path. Keep the current native lexer optimization. Do not
modify the user's minigo checkout or the installed Go toolchain.

First test a transparent, versioned, pointer-free tree snapshot stored on
disk. Each hit reconstructs new script trees in the receiving engine.
Retain node positions, source text, tree ownership, names and numeric flags.
The key includes template name, text, delimiters, parser/codec version and
the effective function-name set. Never persist FuncMap closures, runtime
typedefs or VM values. A failed parse is never cached. Corrupt snapshots
fall back to parsing. Use atomic cache-file publication.

## Implementation boundary

Go's public parse nodes do not expose their source-text and owning-tree
fields. Add experimental snapshot helpers to isolated copies of
`text/template/parse` and a cache wrapper to isolated `text/template`.
The interpreter runs these helpers from source; no host parse trees cross
the boundary. An isolated GOROOT selects these sources for the harness.
JSON is the first snapshot format because it is inspectable and easy to
validate; its interpreted decode cost may negate the gain. Record that
outcome instead of assuming that serialization is free.

## Validation and measurement

1. Focused native and minigo semantic probes: output and execution errors,
   independent parse results, Clone sharing, mutated trees, define order,
   empty redefinitions, function-map replacement, key changes, broken
   templates, corrupt cache files, and complex/numeric node restoration.
2. Compare no-cache, cold-cache and warm-cache whole-process runs, using
   interleaved rounds. Record cache counters and stage timings separately.
3. Run representative examples, then all 53 generate lines. Compare return
   status and every generated file to native output.
4. Retain native-goimports and ordinary interpreted-goimports conditions;
   pin the index lookup for the latter. Keep GOPATH unchanged.

Adopt only if a warm-cache full-generator improvement survives read,
decode, reconstruction, hashing and package-loading costs, with semantic
checks and the 53-line gate passing. This prototype does not establish
general safety for arbitrary `parse.Tree.Parse` callers or persistently
shared mutable ASTs.

## Results, surprises and decisions

### Decision 1: JSON is too expensive (first full strict run)

Cold-cache strict completed successfully in 106.062 s. The interpreted
JSON serialization dominated, including 17.63 s for `union.tmpl` alone.
The first warm run trapped on the pre-existing unbound `strconv.IntSize`
used by base64 decoding. A temporary harness-only constant workaround
exposed a second pre-existing issue: decoded byte slices lack the type
information needed by a `TextNode.Text` initializer (`cannot use slice as
[]byte`). Neither issue is being fixed in minigo in this experiment.

Replace JSON with a compact, length-prefixed wire format. It stores only
fields relevant to each node kind, and parses scalars using bound strconv
functions. This retains byte-exact strings, numeric flags, node identity,
owning-tree pointers and source text without reflection-based decoding.
The saved-cache version changes, so JSON artifacts cannot be mistaken for
the new format. Retain the JSON failure as evidence rather than as a
speed comparison against a successful warm execution.

Initial compact-format strict runs both succeeded: cold 1.889 s, warm
1.387 s. These are single runs with tracing, not final benchmark medians.
Native stdlib tests for both modified packages passed. The focused native
and minigo semantic probe produced matching output, including syntax and
execution-error locations, function-map replacement, tree isolation and
Clone sharing. Further measurement and the 53-line gate remain pending.

### Interleaved strict benchmarks, wire v2 (9 rounds per condition)

Whole-process wall time, no tracing; condition order rotates each round.
All executions exited successfully and reported `err=<nil>`.

| imports condition | no cache | cold cache | warm cache |
|---|---:|---:|---:|
| native goimports | 1.1589 s | 1.6816 s | 1.3753 s |
| interpreted goimports, empty index | 2.1096 s | 2.6314 s | 2.3006 s |

The no-cache condition uses the same installed experimental sources with
the cache disabled, so package-loading and wrapper changes are controlled.
It is not an unmodified-toolchain baseline. Any cost of merely installing
the helper is excluded from the reported cache penalty.

Warm is 18.7% slower with native goimports and 9.1% slower with
interpreted goimports. Cold adds approximately 0.52 s in either condition.
The ordinary condition uses the same interpreter harness without the
imports.Process binding, and an os.UserCacheDir override to an empty
directory, so only the machine-dependent index lookup is changed.
GOPATH, HOME and the module cache are unchanged.

Separate traced compact-format runs covered 40 embedded base files, producing
64 associated templates. There are also eight framework hook files (48
embedded files total); hooks remain on the ordinary parser path.
The cold run spent 0.188 s parsing, 0.491 s encoding, and 0.041 s writing.
The warm run spent 0.015 s reading and 0.371 s decoding/restoring; its
total cache-wrapper time was 0.402 s. It read 775,035 bytes including
checksums. The disk itself is not the dominant cost: construction of the
neutral record graph followed by script-node reconstruction is more
expensive than the current host-lexer-assisted parser.

Decision: do not adopt this persistent-cache implementation. The earlier
52 ms Copy probe omitted the serialization-neutral representation and its
reconstruction, and therefore substantially understated the warm cost.
The repeated benchmark establishes a regression, not an uncertain gain.
Finish correctness checks and retain the prototype as a reproducible
negative result; do not add this cache to minigo's normal execution path.

### Decision 2: pin the generator version in the native oracle

The first full-gate case differed only in the generated version comment:
native built from the scratch directory reported `(devel)`, whereas the
interpreter's module metadata reported
`v2.0.0-00010101000000-000000000000`. Both the no-cache and warm-cache
interpreter outputs agreed. Rebuild native using oapi-codegen's supported
`main.noVCSVersionOverride` linker setting with that exact version. Do not
strip or normalize output: all subsequent checks compare complete bytes
with this explicitly pinned oracle.

### Full 53-line correctness gate, wire v2

All 53 lines succeeded with native, cache disabled and warm cache. Every
written file's complete SHA-256 digest matched the pinned native oracle.
The inventory includes two lines in `import-mapping/samepackage` and two
in `petstore-expanded/fiberv3/api`; each is checked independently.

Single serial whole-process totals (not benchmark medians): native
5.932 s, cache disabled 42.979 s, warm cache 53.796 s. Warm adds 10.817 s,
approximately 25.2%, over the full workload. The disk cache therefore
also fails the intended multi-process amortization case.

Detailed manifests, arguments and individual times are in `gate.json`.

### Decision 3: preserve slice capacity, not just length

An additional public-Tree probe exposed a codec defect: a parsed list
with length/capacity 3/4 was restored as 3/3. The earlier append probe
used a shortened slice, so it exercised aliasing within the existing
length but did not detect this boundary. Capacity is observable, and
appending at the original length can change aliasing if it shrinks.

Wire v3 stores capacities of node/argument/declaration/command slices,
identifier and field-name slices, and text-byte slices. Restoration uses
those exact capacities. This is an experiment-only codec fix, not a
minigo runtime change. Version the cache again, retain the v2 benchmark
and full-gate artifacts under `*-v2.*`, and repeat the final benchmark
and output gate rather than presenting stale timings for the new code.

The prototype preserves fresh parser output. It is not a serializer for
arbitrary user-mutated trees with shared slice backings or a replacement
for arbitrary direct calls to Tree.Parse.

### Final strict benchmarks, wire v3 (9 interleaved rounds)

| imports condition | cache disabled | cold cache | warm cache |
|---|---:|---:|---:|
| native goimports | 1.1624 s | 1.7291 s | 1.4069 s |
| interpreted goimports, empty index | 2.0895 s | 2.6645 s | 2.3539 s |

Warm regresses wall time by 21.0% and 12.7%, respectively. Cold regresses
it by 48.7% and 27.5%. The negative conclusion survives the correctness
fix; this version is not a performance candidate for integration.

These are medians of full new-process executions. The interpreter's
in-process timer is logged for diagnostics, but not used as the benchmark
statistic. `bench.json` contains all 54 observations.

### Final wire v3 gate and validation

All 53 lines match native exactly. Single serial totals: native 5.968 s,
cache disabled 44.529 s, warm 57.704 s (+29.6%). Native modified-stdlib
tests passed under -race, go vet passed for those packages and the
harness, and all differential semantic, corrupt-cache and unavailable-
cache checks passed. The two JSON-path gaps reproduce without the
experimental GOROOT; see bugs/ and bug-harness/.

The consolidated report is final-report.md. No minigo runtime change is
proposed. The complete scratch prototype and raw measurements remain.
