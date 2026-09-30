# minigo

An embeddable interpreter for a large subset of Go, compiled to bytecode
and executed on a lazy stack VM — packages are located, indexed, and
initialized on demand as the script's execution reaches them.

```go
e := minigo.NewEngine(".")
v, err := e.Run(ctx, "./script", "Main") // dir, file, or import path + entry func
```

- lazy per-function compilation: packages are indexed up front, each
  function is compiled to bytecode only when execution reaches it
- scripts are valid Go, verifiable with `gopls`/`go vet`; the compiler
  still never fails — unimplemented constructs emit a trap that fires
  only if execution reaches them, so everything else still runs
- special forms ("quoted Go"): ordinary Go calls dispatch by canonical
  symbol identity to host handlers that receive the call's syntax
  instead of evaluating it — stub packages keep DSL scripts typed and
  gopls-friendly (`examples/convert-define`, `examples/task-run`)
- host boundary: Go values box with reflective method dispatch; stdlib
  intrinsics (`fmt`, `strings`, `os`, `encoding/…`) bind as packages
- generics: monomorphize-on-use, including the Go 1.26/1.27 deltas —
  `new(expr)`, self-referential constraints, generic methods,
  promoted-field literal keys, generalized func-type inference
- introspection: `minigo/inspect` exposes packages, decls, type
  expressions and values to scripts; the REPL has `:cd`/`:ls`/`:pin`
- a Go subset, not full Go — e.g. channels/`select`/`go` model a
  documented single-thread approximation, not real concurrency
  (`TODO.md` tracks coverage and gaps)

A `minigo` CLI is included (`./cmd/minigo`): `minigo run <ref> [--entry F]`,
`minigo repl`, `minigo vet <ref>`, `minigo gen-intrinsics`, or the
`minigo <ref> [func]` shorthand. See `docs/sketch/plan-minigo-vm.md` for the
design, `TODO.md` for current coverage and remaining gaps, and
`examples/task-run` for an embedding example and `examples/convert-define`
for a DSL tool built on special forms.

## Provenance

This repository is a reboot of [`minigo2/`](https://github.com/podhmo/go-scan/tree/main/minigo2)
from `github.com/podhmo/go-scan` (main @ `87cffe1`). It was copied file-wise
(no git history) and renamed `minigo2` → `minigo`; the only carried
dependency is a vendored subset of go-scan's `locator` in `pkg/` (see
`pkg/SOURCE.md`). The earlier tree-walking interpreter that used to live
here was removed.
