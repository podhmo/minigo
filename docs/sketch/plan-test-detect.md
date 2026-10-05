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

## Combined world: detection on the shared pass

The interesting version of this tool is not the standalone binary — it
is detection as a *step inside one scripted run* over the same parse
the other example tools already pay for.

`examples/gen-sync` reads declarations out of `index.Build`, which is
fed by `syntax.ParseFile` (a full parse). `examples/minigo-generate`
shares one package cache across directive calls. `test-detect`, alone,
walks with `parser.ImportsOnly`. Three tools, three passes over the
same tree.

Measured on this repository (117 files, ~1.7MB): imports-only parse
≈ **1.2ms**, the `syntax.ParseFile` mode ≈ **33ms** — a ~28× gap. So:

- Run alone, detection wants its own cheap walk; paying a full parse
  just for imports is a tax.
- Run *with* the others, the accounting flips: `syntax.File.Imports`
  already holds every import, so the dependency graph is a BFS over
  data the index build produced anyway — detection becomes a **free
  query**, not a second walk.

That reframes what the detector is in a scripted world: not a scanner
but a *scheduler input*. The affected set can drive more than
`go test` — scope a `gen-sync` scan to affected directories, dispatch
`//minigo:generate` directives only in affected packages, then test the
same set. Detection decides *where the work lands*; the other tools do
the work. One walk, one parse, three consumers — the shared-cache
thesis from minigo-generate, applied repo-wide.

The standalone CLI stays as built: CI detection that runs before
`go mod download` cannot wait for a full load. What a combined variant
would share is the graph builder — only the source of `(imports,
hasTests)` changes, from "parse files yourself" to "ask the index".

## Spike feedback — what building it taught us

From `examples/test-detect` as it stands, sorted by what kind of
finding each is.

### Verified facts

- **The whole detect run is single-digit milliseconds on this repo** —
  5 modules, 117 files, 67 internal edges in ~2.5ms end-to-end
  (`-verbose` measured). The imports-only walk is ~1.2ms of that; BFS
  and output are noise.
- **Imports-only vs full parse measured ~28×** (1.2ms vs 33ms over the
  same 117 files in the `syntax.ParseFile` mode) — the number behind
  the combined-world accounting above.
- **Dir-level package nodes held**: the external `foo_test` package
  merges into its directory for free, and imports that exist only in
  `_test.go` files create edges — pinned by a unit test where `tdep`
  reaches `b` exclusively through `b/b_test.go`.
- **Multi-module discovery held on the real repo, not just fixtures** —
  `inspect/inspect.go` yields 9 affected packages spanning the root
  module plus `convert-define`, `gen-sync`, and `task-run`; nested
  `go.mod`s are skipped by their parent's walk and get their own.
- **`hasTests` from the `_test.go` filename was enough** —
  `go/build.ImportDir`'s `TestGoFiles`/`XTestGoFiles` split was never
  needed, so nothing lost by not evaluating build constraints.
- **A nice emergent property**: `inspect/` itself has no `_test.go`
  files — its coverage lives in the root package's tests — so touching
  `inspect/inspect.go` surfaces as "test the root package", which is
  the semantics actually wanted.
- **Unresolvable inputs degrade to warnings, empty diffs to empty
  output** — a docs-only `git diff` produces no stdout, exit 0; the
  CI-side `has_tests=false` case falls out without a flag.

### What became clear

- **"Internal" is the union of discovered `go.mod` paths, not one
  module** — this repo's `examples/*` modules `replace`-import the
  root, so a single-module assumption silently drops real edges. The
  pasted spec's one-`moduleName` model would have missed
  `inspect/` → `gen-sync` entirely.
- **Filtering belongs at output, never at traversal** — `-exclude` and
  the untested filter run *after* the BFS; cutting them mid-walk would
  hide dependents of excluded packages (a generated-code hub is exactly
  the case that breaks).
- **Deleted files resolve through their directory** — dir-level nodes
  mean a deleted file in a live dir still seeds correctly; a deleted
  *dir* warns and skips, which is fine because its dependents stop
  compiling anyway (a build-level concern, not test selection).
- **Skipping build-constraint evaluation is the right bias** — parsing
  every `.go` file over-approximates edges (a `_windows.go` import
  still registers on a Linux run). For detection, wider sets cost extra
  tests; missed edges cost coverage. Over-approximation wins.
- **On a repo this size the real win is ordering, not speed** — at
  117 files both parse strategies are instant; what ImportsOnly buys is
  running *before* `go mod download`, on sparse checkouts, with zero
  module resolution. The 28× ratio is what that property compounds
  into on a thousand-package monorepo.

### minigo limitations found

- **None exercised — by construction.** The spike never calls the
  interpreter, which is itself the on-theme answer restated as
  evidence. What a *scripted* detector would need is below.
- **Enumeration is the gap, not the data.** `syntax.File.Imports`
  already retains per-file imports and `inspect.ImportsOf` exposes
  them — but `pkg/locator` only resolves a *named* import inside one
  module; nothing enumerates "every package under a root, across
  nested `go.mod`s" or marks test-file presence at package level. A
  scripted detector has the graph for free once files are loaded; it
  lacks the loader that finds the files.
- **`syntax.ParseFile` is full-parse only** — there is no
  imports-only-flavored entry, so a script can't do the cheap scan;
  it has to ride on someone else's full parse to stay fast (which is
  exactly the combined-world shape).

### Asks of minigo (filed/wished)

- **A repo-enumeration surface** — "all packages under a root, across
  nested modules, with per-package imports and test-file presence" —
  the missing piece that would let detection run as a script step on
  the shared pass. Filed in TODO.md.

### Future work

- **The combined/scripted round** — detection as a query over the same
  index pass the other tools pay for, and the affected set as scheduler
  input for scoped `gen-sync`/directive runs (chapter above).
- **Sharding the affected set** (`-shard k/N`) for parallel CI jobs.
- **CI wiring** — a `has_tests` output so an empty result skips the
  test job, and per-module `go -C` test loops over the JSON format.
