# test-detect

List the packages affected by a set of changed `.go` files — the
packages worth testing — computed in pure Go, in memory, with no
`go list` and no module resolution.

Unlike the other examples, this tool does **not** use the minigo
engine. It is a pure-stdlib experiment: the repository's internal
import graph is built by parsing every `.go` file with
`parser.ImportsOnly` (package clause + import declarations only), then
reverse dependencies are walked from the changed files. Design notes
are in [docs/sketch/plan-test-detect.md](../../docs/sketch/plan-test-detect.md).

## Usage

```console
$ go run . inspect/inspect.go
github.com/podhmo/minigo
github.com/podhmo/minigo/cmd/minigo
...

$ git diff --name-only origin/main HEAD | go run . -stdin
...

$ pkgs=$(go run . -format space inspect/inspect.go) && [ -n "$pkgs" ] && go test $pkgs
```

## Flags

| flag | meaning |
|---|---|
| `-root` | repository root to scan (default `.`) |
| `-stdin` | read changed file paths from stdin (also the default when no args) |
| `-format` | `pkg` (default), `space`, `dir`, or `json` |
| `-exclude` | regexp of import paths to drop from output (repeatable; traversal still propagates through them) |
| `-include-untested` | also list packages without `_test.go` files |
| `-on-unresolved` | `warn` (default) or `all` — what to do when an input cannot be resolved to a scanned package |
| `-verbose` | module/file/edge counts and dropped packages on stderr |

## Behavior notes

- Every `go.mod` under `-root` is discovered; a file belongs to its
  nearest enclosing module, so dependency edges cross module boundaries
  (e.g. `inspect/` → `examples/gen-sync/`).
- `_test.go` imports go into the same graph — changes in test-only
  dependencies are detected.
- `vendor`, `testdata`, and `.`/`_`-prefixed directories are skipped.
- Packages without tests are dropped from the output but still
  propagate impact to their dependents.
- Changed paths that do not resolve to a scanned package produce a
  stderr warning and are skipped — a silently dropped file would
  silently drop test coverage. The same rule applies to the tool's own
  failures: a file whose import block fails to parse keeps whatever
  imports were recovered *and* warns; a truncated stdin read exits
  non-zero rather than answering with a partial change set.
- Resolution is by directory, not by file name — that is what makes
  deleted files work, but a resolving path is not necessarily a correct
  one: a mistyped or cwd-relative input can silently resolve to a
  package it does not belong to (a basename like `typo.go` has no
  directory part and collapses onto the root package). This cannot be
  detected mechanically; feed exact `git diff --name-only` paths.
- Empty result means empty output for `pkg`/`space`/`dir`, so
  `$(test-detect ...)` never expands into "test the current package" —
  callers should still gate on non-emptiness, as in the example above.
  `json` emits `[]` instead: valid JSON a pipeline can still parse.
- `-on-unresolved=all` is the CI-safe mode: if even one input fails to
  resolve — a non-.go file (`go.mod`, docs, `//go:embed` targets, cgo
  headers), a path under a skipped directory like `testdata`, a
  directory argument, or any path that does not exist (deleted files,
  rename old paths, typos) — the selection is untrusted and every
  package is listed instead of risking an empty answer that reads as
  "nothing to test". The usual filters still apply: `-exclude` drops
  matches and `-include-untested` also lists untested packages. Under
  `all` a missing `.go` file no longer resolves through its directory
  (an unverifiable path is indistinguishable from a typo), so a diff
  containing deletions falls back entirely; the default `warn` keeps
  the old skip-with-warning behavior.
