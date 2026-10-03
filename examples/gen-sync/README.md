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
SSoT/sync pattern for free. And because `inspect` reads declarations from
the index — parse cost only — the scan works on real code: the target
package never has to be pure, self-contained, or cheap to load (a `model`
package dragging heavy transitive deps is the normal case). See
`docs/sketch/plan-gen-sync.md` for the design note.

## How it works

```console
$ go run ./          # sync ./app
$ go run ./          # run again: idempotent — 0 file(s) updated
$ go run ./ -check   # report drift without writing (for CI), exit 1 if stale
$ go run ./ -deps    # also rewrite files in followed same-module imports
```

The host (`main.go`) is thin: it parses flags, starts a minigo engine rooted
at the current directory, and calls `script.Main(dir, check, deps)`. All of
the interesting work happens in the script (`script/main.go`), running inside
the interpreter:

1. **Scan** — `inspect.DirOf(dir)` gives the package; `inspect.Files` /
   `inspect.Decls` enumerate declarations. The in-subtree import closure
   is always walked (`inspect.Imports` + `inspect.PackageOf` BFS paths
   under `inspect.Path(root)+"/"`) — it's the search space for
   exploration and enumeration. `-deps` only widens the *write set*:
   without it, only the scanned package's own files are rewritten
   (`app -> app/internal/mood` is always read, synced only with `-deps`;
   `app -> scanx`, the tool's own helper, leaves the subtree and is
   never followed).
2. **Collect** — no magic comments; each declaration's own surface (type
   shape, struct tags, method set, name) — plus what it reaches —
   decides which generators it wants:

   | Signal | Rule | Directive |
   |---|---|---|
   | `type X int`/`string` + a `const` block of `X` in the package | enum | `stringer -type=X` |
   | non-alias interface named `*Service`/`*Store`/`*Client`/`*Repository` | service boundary | `mockgen -source=<file> -destination=mock_<file>` |
   | struct field tag `required:"true"`, or `required` as a `validate:`/`binding:` element — on the struct *or any struct reachable through its field types* | validation candidate, recursively | `requiredgen -type=X` |
   | type declaring `Discriminator() string` | `oneOf` variant | `oneofgen -type=X` |
   | interface requiring `Discriminator() string` | `oneOf` union + implementers | `oneofgen -type=X -variants=a,b,pkg.c` |

   The recursive half lives in `scanx`'s `Explorer`: named type
   references resolve to canonical `path.Name` names, scope-gated to the
   subtree, resolved lazily with per-package caching, and walked BFS
   with a visited set so cyclic type graphs terminate.

   `stringer`/`mockgen` are real tools; `requiredgen`/`oneofgen` are
   hypothetical — the *directives* are the demo's output, not something
   `go generate` is expected to run here.
3. **Rewrite** — collected directives live under a sentinel line in each
   file:

   ```go
   // Code generated directives below are managed by gen-sync. DO NOT EDIT.
   //go:generate stringer -type=Status
   ```

   Each run regenerates the *run* of `//go:generate` lines directly under
   the sentinel, so stale (`-type=Priority` after `Priority` was renamed)
   and orphaned directives disappear without diffing — while hand-written
   directives anywhere else (above the sentinel, or below it but separated
   by a non-directive line) survive. The sentinel itself is recognized at
   code position only, so quoting it inside a `/* */` block or a raw
   string does not open a managed region.

   The script's mechanics — tag parsing, spec-type reads, sentinel/managed-
   region handling — live in `scanx`, a sibling package the script imports
   and the engine interprets like any other module-local source. `scanx`
   papers over the gaps in what `inspect` exposes (const `ValueSpec` types
   and alias-ness aren't on `Decl`, `Pos` is a `"file:line:col"` string,
   `MReqs` traps on non-interfaces) — see the plan doc's limitations list.

## Demo

`app/` is deliberately hostile: `level.go` carries a stale directive,
`status.go` is already correct, `retired.go` manages a type that no longer
exists, and the rest of the package is seeded with distractors —
decoy consts that inherit another enum's type, alias types wearing
matchable names, a `Discriminator() int` and a free `func Discriminator`,
an embed-promoted implementer the collection can't see, tags like
`json:"required,omitempty"` and `notrequired:"true"`, a
hand-written `//go:generate` the sync must not eat, and the sentinel text
itself quoted inside a block comment and a raw string literal.
`graph.go` seeds the exploration itself: pointer/slice/map/generic-arg
indirections, mutual and self cycles that must terminate, an alias hop,
a same-name shadow in another package, and an anonymous-struct bait
that honestly misses. `app/internal/mood` is always read — `Remote`
reaches its `required`-bearing `Marked`, and `Signal` lands in the
`-variants=` list — but is only rewritten with `-deps`;
`app/internal/meta` is reached and matches nothing.

```console
$ go run ./
gen-sync: app/config.go inserted managed block (2 directive(s))
gen-sync: app/events.go inserted managed block (6 directive(s))
gen-sync: app/graph.go inserted managed block (8 directive(s))
gen-sync: app/job.go inserted managed block (1 directive(s))
gen-sync: app/level.go rewrote managed block (1 directive(s))
gen-sync: app/ops.go inserted managed block (1 directive(s))
gen-sync: app/phase.go inserted managed block (1 directive(s))
gen-sync: app/retired.go rewrote managed block (0 directive(s))
gen-sync: app/shapes.go inserted managed block (1 directive(s))
gen-sync: app/status.go up to date
gen-sync: app/store.go inserted managed block (1 directive(s))
10 file(s) updated

$ go run ./
gen-sync: app/config.go up to date
...
gen-sync: app/store.go up to date
0 file(s) updated
```

`make demo` runs both invocations and prints the diff; `make clean` restores
the fixture.

## Layout

- `main.go` — flag parsing + `minigo.NewEngine` + `e.Run(ctx, "./script", "Main", ...)`
- `script/main.go` — the interpreter-executed body (`package script`): the sync *policy* — scan, collect, rewrite
- `scanx/` — the scanning *mechanics* library the script imports (tag/spec parsing, sentinel + managed-region handling, `inspect`-view helpers); interpreted along with the script
- `app/` — the scanned fixture: matching decls mixed with decoys designed to defeat naive matching
- `app/internal/mood/` — same-module leaf package: always read for exploration and variant collection, rewritten only with `-deps`
- `app/internal/meta/` — leaf package reached the same way, matching nothing (and shadowing `app.Inner`'s name)
- `testdata/` — expected post-sync files asserted by `main_test.go`
