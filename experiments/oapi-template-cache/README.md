# oapi-codegen persistent template-tree cache experiment

This directory contains the complete experiment implementation, Python
drivers, Go harnesses, focused tests, original scripts and recorded results.
See [the experiment report](../../docs/sketch/experiment-oapi-template-cache.md)
for the negative performance result and the two independent runtime gaps.

## Prepare and run

Use Go **1.27.1** and an oapi-codegen checkout at
`43281d18a9d0d4adc921a2e697147aaf63fd6479`. The default target is
`~/.cache/minigo-realworld/oapi-codegen`; pass `--target` for another copy.
Go module dependencies must be available locally; the build defaults to
`GOPROXY=off`, or respects an explicitly supplied GOPROXY.

From this directory:

```sh
python3 make_wire.py --work /tmp/oapi-template-cache-repro \
  --target ~/.cache/minigo-realworld/oapi-codegen
python3 build.py --work /tmp/oapi-template-cache-repro --validate
python3 check_semantics.py --work /tmp/oapi-template-cache-repro
python3 run.py representatives --work /tmp/oapi-template-cache-repro
python3 run.py bench --work /tmp/oapi-template-cache-repro
python3 run.py gate --work /tmp/oapi-template-cache-repro
python3 probes.py templates --work /tmp/oapi-template-cache-repro
python3 probes.py bugs --work /tmp/oapi-template-cache-repro
```

`--minigo` and `--goroot` on setup select a different engine checkout or
installed Go root. The engine defaults to this repository; GOROOT is
discovered using `go env GOROOT`. If `--work` is omitted, scripts use
`OAPI_EXPERIMENT_WORK`, or `.work/` in this directory. Build products,
copied dependencies, generated files and new measurements stay there.
The installed toolchain and input target checkout are never modified.

Run benchmarks sequentially. Each cold cache is recreated before its
sample, and condition order rotates through nine rounds. Exit status and
the interpreter error value are both checked. The 53-line inventory and
output paths are checked in under `fixtures/`; no previous `/tmp` files
are needed. Output comparison uses full file SHA-256 digests.

## Files

- `setup.py`: isolate the toolchain and target, generate the original JSON
  codec, and install tests plus the opt-in cache wrapper.
- `make_wire.py`: run setup and generate final wire v3, preserving slice
  capacities. Neither generator rewrites its own checked-in source.
- `build.py`: format and build the oracle, harnesses and probes. Native
  generation explicitly pins its version comment to the interpreter's
  scratch-module version. `--validate` runs race tests and vet.
- `cache_template.go.in`: template cache wrapper used by both codecs.
- `templates/*.go.in`: snapshot, corruption, concurrency and unavailable
  storage tests installed into the isolated standard library.
- `harness/`: minigo runner with optional native goimports and a controlled
  empty-index directory. Its module stays separate from the main package.
- `bug-harness/`: diagnostic runner; only this runner can inject IntSize.
- `semantic/`, `check_semantics.py`: native/minigo, cold/warm, aliasing,
  error-location, corruption and unavailable-storage comparisons.
- `parse-probe/`, `copy-probe/`, `probes.py`: preliminary parse/copy probes
  and the independent bug reproductions in `bugs/`.
- `run.py`: representative traces, interleaved benchmarks and full gate.
- `results/`: the actual measurements, digests, traces and failures from
  the recorded experiment. Reruns write to the workspace, not this archive.
- `archive/`: exact original drivers and the chronological plan/report,
  including their historical machine-specific paths. These files are for
  audit, not the portable entrypoints above. Final generated helper sources
  are retained as `.go.in` files for inspection.

## Revisit the JSON failure

Run setup in a **different workspace** to choose JSON rather than wire:

```sh
python3 setup.py --work /tmp/oapi-template-cache-json \
  --target ~/.cache/minigo-realworld/oapi-codegen
python3 build.py --work /tmp/oapi-template-cache-json
python3 run.py representatives --work /tmp/oapi-template-cache-json
```

JSON cold serialization took about 106 seconds for strict, and the warm
path traps on existing runtime gaps. The driver will reject that error;
the original failing outputs and successful cold trace are in `results/`.

The helpers extend only copied stdlib source for this experiment. This is
not an installed minigo cache API, a production feature or a serializer
for arbitrary user-mutated parse trees.
