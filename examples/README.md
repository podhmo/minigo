# Examples

Each example is a separate Go module demonstrating minigo through
interpreted task definitions, metadata scripts, generators, or application
preview entry points. The exception is
`test-detect`, a pure-stdlib experiment that shares the theme —
Go source as data — without running the interpreter.

## task-run — a mage-style task runner

A task runner in the spirit of [mage](https://magefile.org) /
[go-task](https://taskfile.dev). The **Taskfile is a plain Go file that
minigo interprets**: exported functions with fixed `string` parameters
(or no parameters), returning nothing or one `error`, become runnable
tasks. For example, `func Greet(name string) error` runs as `Greet:Alice`. The file
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
interface names); no marker comments. The scan follows same-module imports
within the target package's subtree; `-deps` expands the write set to those
dependencies rather than only the target package. `-check` reports drift for CI.

See [gen-sync/README.md](./gen-sync/README.md) and the design notes in
[docs/sketch/plan-gen-sync.md](../docs/sketch/plan-gen-sync.md).

## extract-schema — JSON Schema extraction driven by an interpreted script

A port of [go-scan](https://github.com/podhmo/go-scan)'s `tools/genschema`:
emit a JSON Schema for one named type (`./app User`) or a whole package
(`./app` → `$defs`). Unlike the other examples, **the package walker
itself is a minigo script** (`./script` calls `inspect.PackageOf` /
`Decls` / `TypeFields` / …), so the scan logic is improved without
rebuilding the host — the example doubles as the `inspect` API's main
consumer and its behavior corpus lives in
[minigo-usecasefuzz/extract-schema](https://github.com/podhmo/minigo-usecasefuzz/tree/main/extract-schema).
`-plugin <dir>` loads shape-altering config functions from more scripts;
`-stdlib walk` walks into standard-library source instead of stubbing
stdlib types as leaves.

See [extract-schema/README.md](./extract-schema/README.md) and the
design notes in
[docs/sketch/plan-extract-schema.md](../docs/sketch/plan-extract-schema.md).

## minigo-generate — interpreted //minigo:generate directives

A `go generate`-shaped runner that never builds or execs a tool.
A `//minigo:generate <ref> [args...]` comment names a package directory
holding `func Main(args []string) int`; the runner invokes it inside
one shared minigo engine — no process spawn, no build cost, and the
engine's package caches stay warm across directives. The marker is its
own word on purpose: `//go:generate` lines still go to `go generate`,
so a file opts individual tools into interpreted execution. It is the
dual of `gen-sync`: that example *writes* directives it never runs;
this one *reads and runs* directives it never writes.

See [minigo-generate/README.md](./minigo-generate/README.md) and the
design notes in
[docs/sketch/plan-minigo-generate.md](../docs/sketch/plan-minigo-generate.md).

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

## storybook — Storybook-style previews for server-rendered pages (experiment)

What Storybook does for SPA components, applied to a classic
server-rendered MPA: render a page in a real browser for a chosen data
state and adjust its CSS and layout without real data or a running server.
The primitive is an ordinary **zero-argument `func StoryXxx()`** in a file
behind `//go:build story` that prints HTML to stdout. Run it directly with
minigo and open the saved HTML in a browser; no storybook helper or
application server is required.

```sh
cd examples/storybook
make render STORY=StoryUserLongName > /tmp/story-user.html
# With minigo installed, the equivalent direct command is:
minigo run --tags story ./app StoryUserLongName > /tmp/story-user.html
```

Stories come in two kinds:

- **Page stories** (primary) call a page's render function — view data in,
  HTML out — once per data state: a typical user, a hostile name, a very
  long name with many tags, an empty list. No fake store or request.
- **Handler stories** (fallback) call a handler with a fake store and a
  request, for code whose rendering is not split out of the handler.

`cmd/storybook` is an optional helper embedding minigo: `list`, `export`
(saved HTML and an index), and `preview` (HTTP serving with fresh
interpretation on each request). `make test` checks handler HTTP behavior
and that the direct CLI and the helper print the same bodies.

minigo calls Stories by name, including in `package main`, without a test
wrapper or a successful whole-package Go build for uncalled, unfinished
functions. Syntax, initialization, and executed code must still be
supported. The sample app is synthetic, so its output is not proof of
compatibility with an existing application or of visual correctness.
External assets such as the app's CSS still need a suitable serving setup.

See [storybook/README.md](./storybook/README.md) and the design discussion
(Japanese) in
[docs/sketch/ja/experiment-storybook.md](../docs/sketch/ja/experiment-storybook.md).
