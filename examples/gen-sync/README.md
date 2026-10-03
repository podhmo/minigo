# go:generate Directive Sync (`examples/gen-sync`)

`gen-sync` is a code-generation *orchestrator*: a minigo script scans a package
through the `inspect` index layer, infers which declarations want generation
tooling, and rewrites a managed `//go:generate` block in each source file.
Running the generated commands afterwards is still `go generate`'s job — this
tool only keeps the directives in sync with the code.

It is the inverse of `convert-define`: that example reads call-site syntax to
emit new code, while this one walks the package index and writes edits *back
into the same files that were scanned* — the "metadata → generated directive
kept in sync" pattern.

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
2. **Infer** — no magic comments. Each declaration's surface (type shape,
   struct tags, method set, name) decides which generators it opts into:

   | Signal | Rule | Directive |
   |---|---|---|
   | `type X int`/`string` + a `const` block of `X` | enum | `stringer -type=X` |
   | interface named `*Service`/`*Store`/`*Client`/`*Repository` | service boundary | `mockgen -source=<file> -destination=mock_<file>` |
   | struct field tag containing `required` (recursive field walk) | validation candidate | `requiredgen -type=X` |
   | type declaring `Discriminator() string` | OpenAPI-style `oneOf` variant | `oneofgen -type=X` |

   `stringer`/`mockgen` are real tools; `requiredgen`/`oneofgen` are
   hypothetical — the *directives* are the demo's output, not something this
   example expects you to run.
3. **Rewrite** — each file owns one managed region, introduced by a sentinel
   line:

   ```go
   // Code generated directives below are managed by gen-sync. DO NOT EDIT.
   //go:generate stringer -type=Status
   ```

   Every `//go:generate` line below the sentinel is regenerated from scratch
   on each run — stale directives (`-type=Priority` for a type renamed to
   `Level`) and orphaned ones disappear automatically. Files without a
   sentinel gain the block right after the package clause and imports.
   Everything above the sentinel is left untouched.

   Two index-layer gaps are worked around textually: const `ValueSpec` types
   are not on the `inspect.Decl` view (enum detection reads the raw spec
   lines via `inspect.Pos`), and a decl's doc comment is only reached through
   `inspect.Doc` (which already excludes `//go:generate` lines, so inserted
   directives never confuse the next scan).

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
- `script/main.go` — the interpreter-executed body (`package script`): scan, infer, rewrite
- `app/` — the scanned fixture (enums, a tagged struct, a `Store` interface, `Discriminator` types, and non-matching decls)
- `app/internal/mood/` — same-module leaf package, reached only with `-deps`
- `testdata/` — expected post-sync files asserted by `main_test.go`
