# Examples

Each subdirectory is a self-contained Go module that uses `minigo` to
interpret user-supplied Go files at runtime.

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
information for code generation comes from a host-side `go-scan`
(vendored under `convert-define/pkg/`).

See [convert-define/README.md](./convert-define/README.md).
