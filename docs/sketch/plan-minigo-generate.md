# plan-minigo-generate.md — `//minigo:generate` directives executed in-process

Companion to `docs/sketch/plan-task-runner.md` and
`docs/sketch/plan-gen-sync.md`. This note records what
`examples/minigo-generate` is for (issue #390) and what the spike cost
to build.

## Motivation

`go generate` runs each `//go:generate` line as a subprocess: every
directive pays a `go build` plus a process start, and every tool pays
its own `go/packages` load of the workspace. minigo offers a different
deal — **interpret the tool in the same process**:

- no `go build`, no exec — a directive is an `Engine.Call`, so the only
  cost is interpretation
- **shared caches** — the tool package, the scanned package, and the
  inspect/stdlib intrinsics are parsed and indexed once for the whole
  run, not once per directive
- **process isolation anyway** — `Engine.Call` opens a fresh VM and
  process scope per call (goroutines a tool spawns die with its call),
  so one tool's panic or `os.Exit` cannot take the runner or the other
  tools down with it

The directive is `//minigo:generate`, deliberately distinct from
`//go:generate`: a repo keeps using `go generate` for the tools it runs
today and opts individual lines into interpreted execution.

The named target is stringer-class codegen (issue #390). The real
`stringer` is `go/types` + `golang.org/x/tools/go/packages` — both on
the AGENTS.md banned list and unreachable for interpretation. The
minigo-native answer is a stringer-equivalent written against `inspect`
(`DirOf`/`Decls`/`EnumMembers`), which is also the honest demo: it shows
a directive ending in real generated code, not a print statement.

## Where it sits among the examples

| | `task-run` | `gen-sync` | `minigo-generate` |
|---|---|---|---|
| What the script *is* | a trusted build file | a scanning tool over user code | a dispatcher that runs *other* scripts |
| Engine feature exercised | intrinsic-bound stub package, `os`/`exec`, virtual cwd | `inspect` + `os` write intrinsics | `Engine.Call` reuse + `syntax` comment access |
| Data direction | reads parts of a file → executes them | reads the index → edits the same files | reads comments → dispatches to tool packages |
| Trust model | unrestricted | unrestricted | unrestricted (tools write real files) |

gen-sync writes `//go:generate` lines it never runs; minigo-generate is
the counterpart that *executes* directive lines — for tools the
interpreter can host.

## The directive

`//minigo:generate <ref> [args...]`, an exact-prefix line comment
(leading `//minigo:generate`, no space after `//`, like `//go:generate`)
anywhere in a `.go` file — tied to a position, not to a declaration.
Placement was deliberately left unrestricted (a directive inside a
function body works, as it does under `go generate`'s line scan):
restricting to file-header or file-scope comments was considered and
rejected — it adds a filter without removing code, and a header-only
rule would outlaw the canonical `//go:generate stringer -type=X`
directly-above-the-type idiom this tool exists to host.

- **ref** is a package directory resolved relative to the *directive
  file's directory* (`./tools/stringer`, `../shared/tools/x`). Bare
  names with a search-path/registry are deferred — an explicit path is
  how the spike stays self-contained.
- **args** are space-separated tokens with double-quoted strings, the
  same splitting `go generate` documents. No shell expansion, no env
  interpolation — a directive never reaches a shell, so quoting a
  variable does nothing.
- **discovery cannot use `inspect.Doc`**: `//minigo:generate` matches
  Go's directive-comment shape, so `CommentGroup.Text()` drops it like
  `//go:generate`. The runner reads `sf.AST.Comments` (syntax already
  parses with `parser.ParseComments`) — cheap enough, and positions and
  the package clause come along for `GOLINE`/`GOPACKAGE` for free.

## Execution model

One `minigo.NewEngine` per run (rooted at the scan directory,
unrestricted — tools write real files). Then, per directive, in file
position order:

1. resolve `ref` to a dir, `e.Package`/`e.Run` it (the engine's `byDir`
   cache means repeat references load once),
2. deliver per-directive context (below),
3. `e.Call` the tool's entry, collect its `int` exit code and error.

Sequential execution with the engine reused is the point, not a
limitation: the package caches stay warm across calls, which is where
the startup-cost win actually lives. `Call`s are still independent
process scopes, so fan-out is *possible* later — but it needs a
semaphore (the issue's own memory concern is correct: N interpreted
packages + N VMs is not free) and a race-free context channel. Both are
deferred; `task-run`'s `Deps` machinery is the proven pattern when the
time comes.

## Tool contract

```go
// tools/stringer/main.go (package tools, stringer, or main — the name
// is arbitrary; minigo-generate calls the exported entry by name)
func Main(args []string) int
```

- `args` are the directive's arguments — what `argv[1:]` would be in a
  subprocess. A `flag.NewFlagSet` on `args` behaves exactly like a real
  command's `flag.Parse`.
- **`main()` + `os.Args` is deliberately not the contract.** `os.Args`
  is an engine-wide option (`WithArgs` at construction): a shared
  engine can't give each directive its own argv, and a `NewSession`
  per directive would discard the package cache — the whole reason
  this tool exists.
- **file context the `go generate` way**: the runner sets `GOFILE`
  (base name of the file holding the directive), `GOLINE`, `GOPACKAGE`,
  and `GOFILEPATH` (absolute path) in the host env around each `Call` —
  `os.Getenv` inside minigo reads the real host env, so a tool written
  the `go generate` way needs no new vocabulary. This is
  sequential-safe but would race under the deferred parallel path; the
  alternative when parallel lands is a bound `generate` package keyed
  on `runtime.VMCaller` identity.
- **cwd**: the engine's virtual working directory is the scan dir;
  `GOFILEPATH` is absolute so tools can resolve next to the file
  without guessing.
- Tools compile under `go build`/`go vet` too: import the real
  `github.com/podhmo/minigo/inspect` path (the engine binds both the
  module path and `minigo.dev/inspect`), keep stubs the panic-body
  trick. A tool dir that is `package main` with a real `func main()`
  can *also* run under plain `go run` — one source, both runners.

## Demo: `tools/stringer` + `tools/enumvals`

Two demo tools — the issue's "plugins" — each a package dir with a
`Main`: `tools/stringer` (`-type=X` → `func (x X) String()` as a name
switch plus typed fallback, via `DirOf`/`Decls`/`EnumMembers` over
`GOFILEPATH`'s package) and `tools/enumvals` (`-type=X` → a `Values()`
slice in declaration order). The `app/` fixture exercises the shapes a
runner must take in stride: **stacked directives** on `Status` (two
lines, two tools, one type — the multi-command case), solo directives
on `Priority` and `Level`, plus decoys — an alias wearing a matchable
name, a named int with no consts, a directive trailing its type.

## CLI

`minigo-generate [-n] [-x] [-v] [-run re] [dir]` mirrors `go generate`'s
flag surface:

- `-n` — print the directives that *would* run (dry run)
- `-x` — echo each directive as it runs
- `-v` — verbose file reporting
- `-run re` — only run directives whose full text matches the regexp
- `dir` — the package directory to scan (default `.`)

## Non-goals and deferred work

Skipped for scope, not blocked:

- **parallel directive execution** — needs a semaphore cap and a
  race-free context channel (see above).
- **directive ordering/dependencies** — `task.Deps`/`SerialDeps`
  proves the dedup/cycle machinery; wire it only when a real tool pair
  asks for it.
- **bare tool names / registry** — path refs only this round.
- **`main()`/`os.Args` invocation** — superseded by the `Main` contract
  above; revisiting means per-call argv plumbing in the engine.
- **recursion into subpackages** (`go generate ./...` equivalent) —
  one dir per run is enough for the spike.
- **sandboxing** — unrestricted engine, like the other examples;
  `WithAllowedRoots`/`ModeDeny` are the levers when tools run untrusted.
- **moving to `cmd/minigo-generate`** — deliberately still an example
  (issue's own note: "これはcmd/minigo-generateに実装して良い気がする"
  is the eventual home, once the contract settles).
- **the error-friendliness experiment** — deferred to a later round;
  the prepared prompt is below, in the same shape as the task-run /
  gen-sync rounds.

## Deferred experiment: does it stay agent-friendly when inputs break?

Same question as the task-run / gen-sync rounds: when inputs or plugin
definitions break, does the tool return "where, why, and whose fault"
in the *input side's* vocabulary — and refuse to fake success when it
can't? For minigo-generate the input side has three layers the error
must place correctly: the directive (`file:line`, ref, args), the
plugin package (the `Main(args []string) int` contract), and the
interpreted machinery (which must not leak VM-internal vocabulary).
The point of the sweep is separating the findings into **minigo-side
problems worth fixing** (filed in TODO.md) versus tool-side or
by-design behavior.

Seeded by a quick probe (7 malformed inputs injected): failures
already attribute to `directive file:line` and the run continues +
exits 1 — but `undefined: main.Main` and `too many arguments to Main`
don't name the contract violation in plugin vocabulary, the
missing-dir error repeats the same path three times, and near-miss
markers (`// minigo:generate`, `//minigo:generatex`) are skipped
silently with exit 0 — the "pretending success" shape to verify.

Prompt for the round:

    @podhmo/minigo examples/minigo-generateでもexamples/convert-defineのREADME.mdなどを参考に
    入力が壊れた。あるいはプラグインが壊れた場合に「要するに、エージェントに親切なツールとは、
    直接書く場合に得られていたフィードバック（どこが、なぜ、誰のせいで壊れたか）を、入力側の
    語彙で返し直してくれるツールだと考えます。そして、それができないときに、成功したふりを
    しないツールです。」という理想のようにどのような動作をしたら良いかエラーメッセージなど
    から察することが可能か色々な操作を試して確認してください。ここでの色々な操作とは日常的な
    コーディング及び開発時に起きうる不整合情報の混入を含みます。ディレクティブの不備（壊れた
    ref・引数・マーカー表記ゆれ）とプラグイン側の不備（Main契約違反・panic・非0終了・import
    失敗）、スキャン対象パッケージの不備（構文エラー・alias・constの無い型）などいろいろ
    あるでしょう。runner/ツール側の不備とminigo interpreter側の機能不足・不備は分けて洗い出し
    てください。実験レポートを書いてください。ここでPRを作成してください。余力があればその
    レポートを元に改善を試みたPRをstacked PRsとして追加にしてくれませんか。

## Spike feedback — what building it taught us

From `examples/minigo-generate` as it stands:

- **`[]string` does not cross `Engine.Call` unboxed.** Scalars (string,
  int, bool) pass as `runtime.Value` directly, but a host `[]string`
  arg arrives as the raw Go slice and the callee's param coerce traps
  `cannot use []string as []string` — the shape check wants a
  `*runtime.Slice`. The runner boxes it explicitly:
  `&runtime.Slice{Elems: elems}` — task-run already imports `runtime`
  for the same reason. Worth noting upstream: `Call` could apply the
  `goValueOf` unboxing reflect-call results already get, so host
  containers cross the boundary the same way script values do.
- **Directive discovery needed the raw comment table, as predicted** —
  `//minigo:generate` is a directive comment so `CommentGroup.Text()`
  strips it. `sf.AST.Comments` (already retained by `syntax.ParseFile`)
  keeps it, and `c.Text` still carries its `//` vs `/*` marker, so a
  plain prefix check excludes block comments for free.
- **The `Main(args []string) int` contract held up end-to-end** —
  `flag.NewFlagSet` runs under interpretation, so the demo tool parses
  real flags with zero adaptation, and `main()` wrapping `Main` makes
  the same file a working `go run` command. `os.Getenv`/`os.WriteFile`
  intrinsics give the `go generate` env contract for free.
- **Per-directive env needs save/restore**, not just `os.Setenv` — the
  first version leaked `GOFILEPATH` into the host process (and would
  leak it into the next directive). Now the four vars are swapped in
  around the call and restored after — `TestGenerate` pins the
  restoration with a sentinel.
- **An `inspect`-based stringer is genuinely small** (~100 lines):
  `DirOf` → `Files` → `Decls` → `EnumMembers` (source order, which is
  also the declaration order real stringer emits) → emit. The numeric
  fallback's base type comes from `inspect.Def(target).Text`. Output is
  emitted pre-formatted — running `go/format` would mean interpreting
  `go/parser`+`go/printer` inside the tool.
- **No const values — and no comments, for plugins either.** `inspect`
  surfaces declarations, not evaluated values — stringer only needs
  the member *names* for its switch, so the spike never felt it, but a
  tool that needs iota results or explicit values is out of `inspect`'s
  current reach. The same gap applies to doc text: `inspect.Doc` goes
  through `CommentGroup.Text()`, which strips directive comments, so a
  plugin wanting "the comment above this decl" (a common codegen
  input) has no inspect entry point — it would have to re-parse the
  file through `syntax` itself.
- **Plugin imports are limited to bound/interpretable packages.** A
  real `go generate` tool imports anything; an interpreted one
  effectively lives on the stdlib/minigo-bound surface — `flag`, `os`,
  `strings`, `fmt`, `inspect` are all intrinsics or bound packages.
  Notably absent: `go/format` — generated code must be emitted
  pre-formatted (the demo's two-line template care). A plugin that
  needs arbitrary third-party imports is out of scope by construction.
- **Alias-targeted `-type` fails generically.** Verified: pointing a
  directive at `type StatusAlias = Status` yields "no enum consts" —
  `EnumMembers` matches consts typed by the name itself and does not
  follow the alias. Correct-ish, but a real-world footgun worth
  documenting rather than fixing.
- **Env is the *only* per-directive context channel** — which is
  exactly what blocks the deferred parallel path: four process-global
  vars can't carry context to N concurrent Calls. The plan's bound
  `generate` package keyed on `runtime.VMCaller` identity is no longer
  optional for parallelism, it's the design.
- **Interpreted `fmt` is faithful** — `fmt.Fprintf` with too few args
  renders `%!s(MISSING)` exactly like the host toolchain (hit and
  fixed in enumvals' emit).
- **Refs anchored at the directive file's directory** worked naturally —
  `../tools/stringer` inside `app/` resolves without a registry.
- **Stacked directives cost nothing extra** — the demo's `Status` runs
  two tools on adjacent lines, and each is just another independent
  `Call` with its own `GOLINE`; the second tool's package loads through
  the same warm cache. Multiple tools across one file never needed
  special machinery.
- **Tests copy a patched module** like gen-sync's: the fixture's go.mod
  ships `replace github.com/podhmo/minigo => ../../`, and the temp copy
  rewrites it to the absolute checkout so the tool's `inspect` import
  still resolves.
- **Speed was not the hard part.** Scan + three interpreted tool calls
  (each indexing the package via `inspect`, parsing `flag`, writing a
  file) complete in tens of milliseconds in tests — the shared-cache
  thesis holds; the friction was all at the value boundary, not the
  interpreter loop.
