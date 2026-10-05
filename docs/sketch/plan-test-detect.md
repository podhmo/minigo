# plan-test-detect.md — affected test package detection without `go list`

An experiment note for `examples/test-detect`: a small CLI that takes the
list of changed `.go` files (e.g. `git diff --name-only`) and prints the
packages that should be tested — computed in pure Go, in memory, with no
`go list` process and no module resolution.

## Motivation

The approach comes from two sources:

- A [Zenn article](https://zenn.dev/innovation/articles/e84f8ccca8e6da)
  that cut PR CI time ~73% by testing only affected packages. Its
  detection step is shell glue: `git diff` → `go list` per directory →
  `go list -deps -test` for the whole repo → a Python script that walks
  the reverse dependency graph → `grep -v` to drop generated code.
- A [SpeakerDeck talk](https://speakerdeck.com/sivchari/go-test-o-hayaku-suru)
  (LayerX, @sivchari) showing the same mechanism done natively:
  `go/build.ImportDir` + `parser.ImportsOnly` reads only the package
  clause and import declarations, so no type check, no build, no module
  resolution, no network. Detection finishes in tens of seconds on a
  monorepo with thousands of packages — including sparse checkout, and
  without `go mod download`.

The experiment: merge the two — the diff-driven input of the article
with the AST-entry-point parsing of the talk — as one self-contained Go
tool, replacing the `go list` + Python glue entirely.

It also matches this repository's own constraint: `AGENTS.md` already
bans `go list` and `go/packages`, so a source-level import graph is the
native way to build this kind of tooling here.

## What it does

```text
changed .go files (argv or stdin)
        │
        ▼
walk repo → per-module scan (go.mod discovery)
        │   each .go file: parser.ImportsOnly → package name + imports
        ▼
dir-level package nodes + reverse-dependency edges
        │   (imports outside the discovered module paths are dropped)
        ▼
BFS from the changed files' packages over reverse edges
        │
        ▼
affected package set → filters → stdout
```

### I/O

```console
$ test-detect inspect/inspect.go resolve/env.go
github.com/podhmo/minigo/inspect github.com/podhmo/minigo/resolve ...

$ git diff --name-only origin/main HEAD | test-detect -stdin
...
```

- Input is file paths (relative to `-root`, or absolute). Non-`.go`
  paths and files outside the scanned graph are skipped with a stderr
  warning — a deleted file in a still-existing directory still resolves
  through its directory.
- Default output is one import path per line; `-format space` for
  `go test $(...)`, `-format dir` for repo-relative directories, and
  `-format json` (`[{moduleDir, modulePath, packages}]`) for CI that
  needs per-module `go -C` invocations.
- `-exclude <regexp>` (repeatable) drops matching package paths from the
  output — the article's `ogen`/`gorm` generated-code case. Filtering is
  output-only; traversal still passes through excluded packages, so
  dependents of generated code are not lost.
- `-include-untested` keeps packages without tests in the output. By
  default they are dropped (the talk's third optimization): a `_test.go`
  file in the directory is the test-presence signal, known at scan time
  for free. Untested packages still propagate the BFS — a changed leaf
  must reach tested dependents through untested intermediaries.
- `-verbose` reports module/file counts and scan timing on stderr.

### Design notes and deviations from the reference spec

- **Dir-level package nodes.** `go test` is directory-granular anyway,
  and the external test package (`package foo_test`) lives in the same
  directory. Imports from `_test.go` files go into the same graph —
  changes in test-only dependencies are detected, matching the talk.
- **No `.test` / `_test` normalization.** The pasted spec carries
  `NormalizePackage` for the synthetic `foo.test` / `foo.test [foo.test]`
  entries that `go list -test` emits. A source-level graph has no such
  nodes, so the step is dropped.
- **Multiple modules.** This repository is not one module — the root
  plus each `examples/*` module (which `replace` the root and import its
  packages). The tool discovers every `go.mod` under `-root`, assigns
  each file to its nearest enclosing module, and treats the union of
  discovered module paths as "internal", so edges cross module
  boundaries (e.g. `inspect/` → `examples/gen-sync/`).
- **`parser.ImportsOnly` per file** rather than `go/build.ImportDir`.
  `ImportDir` evaluates build constraints via `build.Context`
  (GOOS/GOARCH suffixes, `//go:build` lines), which silently drops
  files — and their import edges — for other platforms. Parsing every
  `.go` file unconditionally over-approximates edges instead: slightly
  wider, never missing. The `hasTests` signal is derivable from the
  `_test.go` filename, so nothing `ImportDir` offers is lost.
- Skips `vendor`, `testdata`, and `.`/`_`-prefixed directories (the go
  tool's own conventions).

## Verification

- Unit tests build synthetic module trees in `t.TempDir()` and assert
  the affected set with go-cmp: linear chains, diamonds, test-only
  edges, nested modules, deleted files, `-exclude`, untested filtering.
- Then run the tool against this repository itself with real diffs.

## Deferred (future work)

- **Splitting the affected set further** (e.g. `-shard k/N`) for
  parallel CI jobs — the request's own "soreha future work".
- CI wiring: `has_tests` output so an empty result skips the test job,
  per-module `go -C` test loops.
- Build coverage for affected-but-untested packages is out of scope by
  design — the talk assigns that role to a separate all-packages build
  workflow.
- The hub-package pathology from the talk (a generated registry
  importing every service) is fixed by restructuring, not detection —
  out of scope, but `-exclude` is the cheap mitigation.

## Is this on-theme for minigo?

Honest answer, asked last as requested: **partially.**

- The tool does not need the interpreter. Nothing in it is a script or
  a DSL; `minigo` the engine is unused. In that sense it is a standalone
  OSS-shaped CLI that happens to live in `examples/`.
- But the theme of the examples — Go-source-as-data tooling that never
  compiles the target — is exactly what this is. `parser.ImportsOnly`
  is the extreme of that: it reads the AST's entry point and stops.
  It also respects the repo's `go list`/`go/packages` ban in a way the
  article's shell pipeline cannot.
- There is a plausible dogfooding path: this repository's own PR CI
  runs `make test` over everything; the tool could select affected
  packages per module. The repo is small enough that the win is modest,
  but it exercises the tool honestly.
- A further stretch — running this detector *inside* minigo (the script
  interpreted) — is not pursued; `go/parser` coverage in the interpreter
  is not the question being asked.

So: valid as an `examples/` experiment about source-level analysis;
if it graduates to a product, it wants its own repo.
