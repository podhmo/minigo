# minigo

An embeddable interpreter for a large subset of Go, compiled to bytecode
and executed on a lazy stack VM — packages are located, indexed, and
initialized on demand as the script's execution reaches them.

```go
e := minigo.NewEngine(".")
v, err := e.Run(ctx, "./script", "Main") // dir, file, or import path + entry func
```

A `minigo` CLI is included (`./cmd/minigo`): `minigo run <ref> [--entry F]`,
`minigo repl`, `minigo vet <ref>`, `minigo gen-intrinsics`, or the
`minigo <ref> [func]` shorthand. See `sketch/plan-minigo-vm.md` for the
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
