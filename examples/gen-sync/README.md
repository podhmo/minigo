# go:generate Directive Sync (`examples/gen-sync`)

A minigo usage neither `task-run` nor `convert-define` covers: read
**package metadata through `inspect`** — the bound package that answers
"which declarations live in this package" (`DirOf`/`Files`/`Decls`/`Fields`/
`Methods`/`Imports`/`PackageOf`) — and put it to work. The script walks a
package, collects declarations that match a condition from their own
surfaces, and rewrites a managed `//go:generate` block in each scanned
file — the declaration is the source of truth, the directive is kept in
sync with it.

Emitting `//go:generate` (rather than generating code itself) is the lazy
shape of the demo: most of the "anything" metadata collection enables is
already done by existing tools, so writing the *directive* exercises the
SSoT/sync pattern for free. See `docs/sketch/plan-gen-sync.md` for the
design note.

## How it works

```console
$ go run ./          # sync ./app
$ go run ./          # run again: idempotent — 0 file(s) updated
$ go run ./ -check   # report drift without writing (for CI), exit 1 if stale
$ go run ./ -deps    # also follow same-module imports transitively
```

The host (`main.go`) is thin: it parses flags, starts a minigo engine rooted
at the current directory, and calls `script.Main(dir, check, deps)`. All of
the interesting work happens in the script (`script/main.go`), running inside
the interpreter:

1. **Scan** — `inspect.DirOf(dir)` gives the package; `inspect.Files` /
   `inspect.Decls` enumerate declarations. With `-deps`, `inspect.Imports` +
   `inspect.PackageOf` BFS the same-module import closure (the
   `app -> app/internal/mood` edge is only followed then).
2. **Collect** — no magic comments; each declaration's own surface (type
   shape, struct tags, method set, name) decides which generators it wants:

   | Signal | Rule | Directive |
   |---|---|---|
   | `type X int`/`string` + a `const` block of `X` | enum | `stringer -type=X` |
   | interface named `*Service`/`*Store`/`*Client`/`*Repository` | service boundary | `mockgen -source=<file> -destination=mock_<file>` |
   | struct field tag containing `required` | validation candidate | `requiredgen -type=X` |
   | type declaring `Discriminator() string` | OpenAPI-style `oneOf` variant | `oneofgen -type=X` |

   `stringer`/`mockgen` are real tools; `requiredgen`/`oneofgen` are
   hypothetical — the *directives* are the demo's output, not something
   `go generate` is expected to run here.
3. **Rewrite** — collected directives live under a sentinel line in each
   file:

   ```go
   // Code generated directives below are managed by gen-sync. DO NOT EDIT.
   //go:generate stringer -type=Status
   ```

   Every `//go:generate` below the sentinel is regenerated from scratch on
   each run, so stale (`-type=Priority` after `Priority` was renamed) and
   orphaned directives disappear without diffing. The sentinel is a
   safeguard, not the feature: it keeps the tool from destroying
   user-written directives above it — those are never touched. Files
   without a sentinel gain the block after the package clause and imports.

   Along the way the script works around a few gaps in what `inspect`
   exposes (const `ValueSpec` types and alias-ness aren't on `Decl`,
   `Pos` is a `"file:line:col"` string) by reading raw lines at
   `inspect.Pos` coordinates — see the plan doc's limitations list.

## Demo

`app/` is deliberately out of sync: `level.go` carries a stale directive,
`job.go` has none, `status.go` is already correct, and
`app/internal/mood` only lights up with `-deps`.

```console
$ go run ./
gen-sync: app/job.go inserted managed block (5 directive(s))
gen-sync: app/level.go rewrote managed block (1 directive(s))
gen-sync: app/status.go up to date
2 file(s) updated

$ git --no-pager diff examples/gen-sync/app/
# + managed block in job.go, -type=Priority -> -type=Level in level.go

$ go run ./
gen-sync: app/job.go up to date
gen-sync: app/level.go up to date
gen-sync: app/status.go up to date
0 file(s) updated
```

`make demo` runs both invocations and prints the diff; `make clean` restores
the fixture.

## Layout

- `main.go` — flag parsing + `minigo.NewEngine` + `e.Run(ctx, "./script", "Main", ...)`
- `script/main.go` — the interpreter-executed body (`package script`): scan, collect, rewrite
- `app/` — the scanned fixture (enums, a tagged struct, a `Store` interface, `Discriminator` types, and non-matching decls)
- `app/internal/mood/` — same-module leaf package, reached only with `-deps`
- `testdata/` — expected post-sync files asserted by `main_test.go`
