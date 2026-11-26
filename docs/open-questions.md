# Open questions

Decisions the working sessions could not settle on their own. One entry
per question: what is blocked, the options, and who decides.

- **Should a parent's explicit `Bind` of a stdlib path propagate to `NewSession`?** A session builds its own lazy tables, and paths the session still holds pending skip the parent's copy — so `e.Bind("os", custom)` followed by `e.NewSession()` gives the session the default `os` table, not the custom one. This predates lazy install: under eager install the session bound stdlib itself at construction, so the parent's re-Bind never reached it either. But the `NewSession` doc says inherited host bindings "resolve identically", which arguably covers a custom re-Bind of a stdlib path. Fixing it needs the registry to track explicit-vs-default binds so the session prefers the parent's custom table. (Escalated from Devin Review on [#1150](https://github.com/podhmo/minigo/pull/1150).)

## M4-4: do `runtime.HostNamedIs` / `runtime.HostTwinOf` count as removable twin helpers? (resolved)

Resolved 2026-11-19 (user): both stay as the residual reflect.Type↔typedef
fallback — a two-stage capability injection to remove them was judged
too complex to be worth it. Their removal is tracked as a separate
task in docs/next/ja/TODO.md ("intern 表を拡張して HostNamedIs/HostTwinOf
を除去する"), to revisit once the intern table covers un-interned and
stamp-less typedefs.

## src-netip: can a bound-mode-only stdlib package ever run under `--src`?

`ModeSource` loading of `net/netip` traps at package init:
`unique.Make(addrDetail{})` (netip.go:69) calls `unique.Make[T]` — a
generic builtin whose `T` is the unexported `addrDetail` — which lowers
to `abi.TypeFor` (a compiler intrinsic) plus `unsafe.Pointer(&a)`
reinterpretation. None of that is bindable or source-interpretable:
the intrinsic has no body to run, and the unsafe aliasing defeats the
interpreter's value model.

Options:

- Give the interpreter its own intrinsics for `unique.Make`/`abi.TypeFor`
  (a per-package escape hatch, like asmimpl but for generics).
- Declare `net/netip` bound-only by policy and document that `--src`
  for it is unsupported (the bound path already works).
- A general `unsafe.Pointer` story for source mode — much larger scope;
  pointer-reinterpret casts have no interpreter model.

Decision: needs podhmo. `--src net/netip` reproduces at
`unique.Make(addrDetail{})` — bound `netip` stays the parity path.

## readfile-mem: which slice model closes the os.ReadFile memory gap?

A script `[]byte` stores one 16-byte `runtime.Value` per element, so a
32 MB `os.ReadFile` peaks near 560 MB RSS (issue #950). Two candidate
designs, each with real cost:

- Compact `[]byte` backed by the host `[]byte`, materializing element
  cells only on script writes — needs lazy element ops at ~280
  `.Elems` sites (index, len, cap, append, copy, range, spread).
- Boxing results as `*runtime.GoValue` and adding a `string(b)` fast
  path — boxing breaks `b...`, `copy`, `[]byte(b)`, and `slices.*`
  (each needs its own special case; the gap keeps widening).

Decision: needs podhmo — it is a memory-model tradeoff, not a local fix.

PR [#1438](https://github.com/podhmo/minigo/pull/1438) recommends the
compact option as a staged design: first a read-only compact byte slice
that materializes on writes, then native append/copy/write support. That
is a recommendation to weigh, not a decision. The direct `.Elems` count
is now about 313 non-test references, so the mechanical accessor-split
cost is part of the tradeoff.

## exit-codes: what process contract should panic and trap use?

`cmd/minigo` currently exits 1 for both script panics and runtime traps.
A compiled Go binary exits 2 for an unrecovered panic, while `go run`
itself reports the child status and exits 1; difffuzz therefore uses a
compiled oracle binary when it compares exit codes. Usage errors already
exit 2 in the CLI.

Options:

- Keep every run failure at exit 1 (status quo).
- Match a compiled Go binary for script panics: exit 2.
- Split the failure kinds: panic exit 2, minigo runtime trap exit 3, so
  CI can distinguish a script bug from unsupported interpreter surface.

Decision: needs podhmo. This blocks the U4 item in
`docs/sketch/ja/improvement-ideas.md`.

## check-command: should deep trap enumeration be `minigo check` or `vet --deep`?

`minigo vet` already reports unregistered stub calls and performance
hints, but it does not eager-compile function bodies. A deep checker
could compile only the target package with strict lazy compilation and
list embedded `OpTrap` sites as `file:line:col`, without eagerly loading
imports. The open part is the command shape and contract, not the need.

Options:

- Add a separate `minigo check` command with `--json` output.
- Keep one static-analysis entry point as `minigo vet --deep`.
- Leave this to gopls/editors and keep vet focused on minigo-specific
  host-boundary mistakes.

Decision: needs podhmo. The choice affects whether F1 in
`docs/sketch/ja/improvement-ideas.md` is a small CLI addition or a new
public command.

## test-command: should minigo ship a test runner, and what is the minimum contract?

`tools/tmpltests` already proves part of the mechanism: it copies stdlib
tests into a scratch GOROOT, renames `*_test.go`, generates a driver, and
uses a small `testing` shim. A user-facing `minigo test` would turn that
machinery into a supported command, but compatibility boundaries are a
product decision.

Options:

- Ship a minimal runner: internal test files, `TestXxx(*testing.T)`,
  `Errorf/Fatalf/Run/Helper/Cleanup/TempDir/Skip`, `go test -v`-shaped
  output; benchmarks, fuzzing and `TestMain` explicitly unsupported.
- Keep test execution as an internal conformance harness and do not add
  a public command.
- Start with an experiment command or example before committing to CLI
  compatibility.

Decision: needs podhmo. This blocks F2 in
`docs/sketch/ja/improvement-ideas.md`.

## perf-gate: which performance contract deserves CI enforcement?

The repository has benchmarks, but no continuous performance gate. The
suggested direction in PR #1438 is a small representative bench suite
compared with `benchstat`, plus a hard gate for startup time because
startup is central to the product pitch. The cost and noise budget are
not settled.

Options:

- Gate only startup time; keep other benchmarks informational.
- Run a small benchmark set on CI and comment on regressions above a
  threshold without failing the PR.
- Do not add CI performance checks; keep measurements attached to
  individual optimization PRs.

Decision: needs podhmo. This blocks P5 in
`docs/sketch/ja/improvement-ideas.md`.

## typed-call-api: what conversion contract should `CallAs[T]` have?

`Engine.Call` returns `runtime.Unwrap(r)`, so simple scalar results are
already easy to consume. A generic `CallAs[T]` helper would need a deep
conversion contract for script composites, named host values and typed
nils; otherwise it only hides the easy cases and still fails on the hard
ones.

Options:

- Implement only scalar/simple conversions and document the limit.
- Implement reflection-based deep conversion into arbitrary host types.
- Do not add the helper until a real embedding use case requires it.

Decision: needs podhmo. This blocks F5 in
`docs/sketch/ja/improvement-ideas.md`.
