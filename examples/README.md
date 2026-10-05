# Examples

Each subdirectory is a self-contained Go module that uses `minigo` to
interpret user-supplied Go files at runtime. The exception is
`test-detect`, a pure-stdlib experiment that shares the theme —
Go source as data — without running the interpreter.

## task-run — a mage-style task runner

A task runner in the spirit of [mage](https://magefile.org) /
[go-task](https://taskfile.dev). The **Taskfile is a plain Go file that
minigo interprets**: exported `func()`, `func() error`, and
`func(...string) error` declarations become runnable tasks. The file
imports a stub `task` package so it typechecks under ordinary Go
tooling, but `task-run` binds that package's members to host intrinsics
and evaluates the Taskfile in the interpreter — including `Deps`,
`Sh`, `Output`, and friends.

See [task-run/README.md](./task-run/README.md) and the design notes in
[docs/sketch/plan-task-runner.md](../docs/sketch/plan-task-runner.md).

## convert-define — struct conversion generator

A generator that emits struct-to-struct conversion functions from a
declarative definition file. The definition file is statically valid Go
(usually behind `//go:build codegen`) that **minigo interprets**:
`define.Convert` / `define.Rule` calls reach the engine as quoted
special-form calls (`OpSpecialCall`), so the file's body is inspected as
AST and the `define` package itself is never loaded or evaluated. Type
information for code generation comes from the host-side `inspect`
layer.

See [convert-define/README.md](./convert-define/README.md).

## gen-sync — go:generate directive sync from package metadata

A scanning tool that reads package metadata through `inspect`
(`DirOf`/`Files`/`Decls`/`Fields`/`Methods`/`Imports`/`PackageOf`) and
rewrites a managed `//go:generate` block in each scanned file — the
inverse shape of convert-define: **the index is read and edits are written
back into the same files that were scanned**. Targets are inferred from
declarations themselves (enum shapes, struct tags, method sets,
interface names); no marker comments. `-deps` follows same-module
imports transitively; `-check` reports drift for CI.

See [gen-sync/README.md](./gen-sync/README.md) and the design notes in
[docs/sketch/plan-gen-sync.md](../docs/sketch/plan-gen-sync.md).

## test-detect — affected test package detection

Given the changed `.go` files of a commit (e.g. `git diff --name-only`),
print the packages whose tests the change can break. The repository's
internal import graph is built by parsing every `.go` file with
`parser.ImportsOnly` — package clause and import declarations only — so
no `go list`, no module resolution, no network. Reverse dependencies
are walked from the changed files, across this repository's nested
modules; packages without tests are dropped from the output by default.

See [test-detect/README.md](./test-detect/README.md) and the design notes in
[docs/sketch/plan-test-detect.md](../docs/sketch/plan-test-detect.md).
