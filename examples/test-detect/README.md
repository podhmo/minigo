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
- Empty result means empty output (no padding), so `$(test-detect ...)`
  never expands into "test the current package" — callers should still
  gate on non-emptiness, as in the example above.
