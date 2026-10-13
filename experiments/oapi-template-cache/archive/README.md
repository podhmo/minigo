# Persistent template-tree cache experiment

The experiment and scratch artifacts live entirely in this directory.
The original minigo checkout and installed Go toolchain are unchanged.

- `report.md`: pre-implementation plan, findings, measurements and decision.
- `setup.py`: isolate GOROOT and oapi-codegen, generate the first JSON codec.
- `make_wire.py`: generate the compact wire codec and install the wrapper.
- `goroot/src/text/template/experiment_cache.go`: opt-in cache wrapper.
- `goroot/src/text/template/parse/experiment_snapshot.go`: graph snapshot,
  restoration and wire codec, interpreted by minigo.
- `semantic/main.go`: differential semantic probes.
- `harness/main.go`: current-checkout interpreter runner, native goimports
  enabled by default; ordinary mode pins an empty index directory through
  a harness-only `os.UserCacheDir` override and leaves GOPATH unchanged.
- `run.py`: interleaved process benchmarks, six representative traces,
  and the 53-line output gate.

## Repeat existing measurements

```sh
python3 /private/tmp/oapi-template-cache-experiment/run.py bench
python3 /private/tmp/oapi-template-cache-experiment/run.py representatives
python3 /private/tmp/oapi-template-cache-experiment/run.py gate
```

The runner rejects nonzero exit status and any interpreter error even
when a harness accidentally exits successfully. Native output is the
oracle for each generated file, compared using full SHA-256 digests.
The 53-line inventory and written-file manifest are read from the
previous experiment's `/private/tmp/oapiperf/lines.txt` and `all.native`.
Native and minigo runs are sequential; do not benchmark simultaneously
with the correctness gate.

Cache mode is controlled by `OAPI_TREE_CACHE`; an unset variable takes
the ordinary Parse path. `OAPI_TREE_TRACE=1` prints per-template timings
to stderr. The cache wrapper is used only for embedded base templates;
user templates and hooks retain the normal Parse path.

## Rebuild

The scripts hard-code the current machine's pinned Go 1.27.1 toolchain,
oapi-codegen scratch source, and minigo checkout. `make_wire.py` re-copies
the target sources; do not run it over active generation or measurement.
It does not alter the original sources.

```sh
python3 /private/tmp/oapi-template-cache-experiment/make_wire.py
GOROOT=/private/tmp/oapi-template-cache-experiment/goroot \
  GOPROXY=off GOCACHE=/private/tmp/oapi-next-ZMz79f/gocache \
  go -C /private/tmp/oapi-template-cache-experiment/src build \
  -ldflags=-X=main.noVCSVersionOverride=v2.0.0-00010101000000-000000000000 \
  -o /private/tmp/oapi-template-cache-experiment/oapi-native ./cmd/oapi-codegen
GOPROXY=off GOCACHE=/private/tmp/oapi-next-ZMz79f/gocache \
  go -C /private/tmp/oapi-template-cache-experiment/harness build \
  -o /private/tmp/oapi-template-cache-experiment/harness-bin .
```
