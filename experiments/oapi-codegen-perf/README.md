# oapi-codegen perf experiment: scripts

The hand-written tools behind
[docs/sketch/experiment-oapi-codegen-perf.md](../../docs/sketch/experiment-oapi-codegen-perf.md).
They were kept in a scratch directory during the experiment and are
preserved here as they were used, with machine-specific paths replaced by
`go env` lookups. They are not part of minigo's build: the Go sources are
`.go.in` templates or live in their own module.

## Workspace

Every script assumes the scratch workspace `/tmp/oapiperf`:

- `/tmp/oapiperf/src`: a scratch copy of oapi-codegen @ `43281d18a9d0`
  (realworld's `oapi-codegen-examples` pin, from
  `~/.cache/minigo-realworld/oapi-codegen`). Runs rewrite generated files,
  so never point the scripts at the cache itself.
- `/tmp/oapiperf/grafana-src`: grafana @ `fa8d6e65` for the
  grafana-openapi side check (`TARGET_DIR` of realworld's task).
- Native oracles: realworld's `task.sh native` output for the 53 lines,
  and `go run` output for grafana-openapi.
- Binaries: `minigo-<label>` (CLI, `go build ./cmd/minigo`) and
  `h-<label>` (the harness below), one per variant measured.

## Scripts

| file | use |
|---|---|
| `noidx.sh CMD…` | run with an empty `HOME`, so goimports finds no module index (step 2) |
| `run1.sh BIN DIR ARGS…` | one `go:generate` line, wall time |
| `bench.sh ROUNDS DIR "ARGS" label=cmd…` | interleaved CLI rounds under `noidx.sh`, median per label |
| `hbench.sh ROUNDS DIR "ARGS" label=bin…` | interleaved harness rounds (native goimports), median per label; checks `err=<nil>` |
| `withsrc.sh EXTRA BIN ARGS…` | add packages to the first `--src` list |
| `dk.sh WORKDIR CMD…` | run in `golang:1.27-alpine` for Linux profiles (step 7) |
| `bisect.sh`, `bisect2.sh` | `git bisect run` drivers for the step 0 regressions |
| `cases.txt` | the six representative lines (`DIR ARGS`) |
| `prof/build.sh MINIGO_DIR OUT` | build the harness against a minigo checkout (`SRCMAIN=` picks another `.go.in`) |
| `prof/main.go.in` | harness: runs cmd/oapi-codegen in-process with the seven `--src` modes, native `imports.Process`, optional CPU/alloc profiles (`-out`, `-n`) |
| `prof/main_stat*.go.in` | harness variants used with instruction-counting builds |
| `probes/` | small repros diffed against `go run` (review findings, TODO entries) |

Typical strict comparison:

```sh
bash prof/build.sh ~/ghq/github.com/podhmo/minigo /tmp/oapiperf/h-a
bash hbench.sh 9 petstore-expanded/strict/api \
  "--config=server.cfg.yaml ../../petstore-expanded.yaml" \
  a=/tmp/oapiperf/h-a b=/tmp/oapiperf/h-b
```

Correctness gate (from realworld's checkout):

```sh
TARGET_DIR=/tmp/oapiperf/src bash noidx.sh \
  bash tasks/oapi-codegen-examples/task.sh minigo /tmp/oapiperf/minigo-a > all.a
diff all.native all.a
```
