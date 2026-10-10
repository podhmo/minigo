# tmpltests

Runs Go's own stdlib test files **verbatim** under minigo: the target
package's upstream sources are copied into a scratch GOROOT next to their
(renamed) `*_test.go` files, a generated driver calls each `func
TestXxx(*testing.T)` in its own minigo process, and per-test verdicts are
collected. Unlike difffuzz's generated probes, the corpus here is the real
upstream suite — a FAIL is Go behavior minigo gets wrong, a TRAP is
unimplemented surface that became backlog.

Design notes: [docs/sketch/ja/exploration-template-src-tests.md](../../docs/sketch/ja/exploration-template-src-tests.md).

```
make tmpltests                               # text/template, all tests
make tmpltests TMPLTESTS_ARGS="-only TestExec"
go -C ./tools/tmpltests run ./ -h            # all flags
```

`-only <re>` filters at copy + driver time, so an uncompilable test file
can be routed around — but helper functions shared between test files
still follow the copied set, so widen the regex if a needed helper file
was filtered out.

## How it works

1. Build `cmd/minigo` (or use `-minigo <bin>`).
2. Assemble a scratch GOROOT at `<work>/goroot`: every `$GOROOT/src` entry
   is a symlink except `testing`, `flag`, `iter` (replaced by shims in
   `shims.go`) and the target package's parents. The target package itself
   is a real directory: symlinks to upstream `.go` files, `testdata/`, plus
   renamed copies of the selected test files (`exec_test.go` →
   `exec_srctest.go` — minigo's loader drops `*_test.go`) and a generated
   `zzz_driver.go` (`RunAll`/`Run1` + a `name → func` table scanned from
   the test files).
3. For each test: `GOROOT=<work>/goroot minigo run <work>/main --src
   <pkg> -- <TestName>` with cwd set to the copied package dir so
   `testdata/` resolves. One test per process keeps a minigo trap from
   taking down the rest of the suite.

## Verdicts

| verdict | meaning |
|---|---|
| PASS | driver reported the test green |
| FAIL | `t.Error/Fatal` fired — output/error divergence |
| PANIC | a script panic escaped the test (e.g. a reflect.Value misuse that go's reflect would not produce) |
| SKIP | `t.Skip` |
| TRAP | `minigo: runtime trap:` — unimplemented surface |
| HANG | `-timeout` (default 60s) exceeded |

## Rewrites

`main.go:fileRewrites` patches the copied test files where a minigo
limitation would otherwise kill the whole file at init/compile time. Each
rewrite must preserve the test's intent and is tracked as a bug:

- `exec_test.go`: `unsafe.Pointer` → `any` — no `unsafe.Pointer` type yet;
  the rows that use it keep their truth values ({ptr, true}, {nil, false}).

`main.go:srcRewrites` applies the same copy+patch mechanism to non-test
source files (files not listed are symlinked verbatim):

- `exec.go`: `var maxExecDepth = initMaxExecDepth()` → `var maxExecDepth = 250`
  — upstream caps at 100000 but minigo's interpreter frame limit (10000)
  always fires first, so `TestMaxExecDepth` could never observe template's
  own `exceeded maximum template depth` error. 250 exercises the real
  guard well under the frame limit.

## Current gaps (surfaced by the first run)

- `fmt.Sscan` unbound — `parse/node.go` needs it for complex literals;
  blocks `TestExecute` and `TestComparison` wholesale.
- `errors.AsType`, `os.DirFS` unbound.
- `errors.AsType` was bound via generic builtins (#774).
- Value-family FAILs (`reflect.Value.IsNil on struct Value`, `.Int on int
  Value`, `<template.V Value>` for an indexed struct) — the minireflect
  boxing issue from TODO.md.

## Excluded

- `link_test.go` — drives the Go toolchain itself (`internal/testenv`,
  `os/exec`); meaningless here.
- `example*_test.go` — `package template_test` external tests need a second
  package dir; later.
