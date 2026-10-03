# plan-gen-sync.md — keeping `//go:generate` directives in sync from the inspect index

Companion to `docs/sketch/experiment-convert-define-on-inspect.md` and
`plan-package-introspection.md`. This file explains what `examples/gen-sync`
is for and why it is built the way it is. (Retrospective: this note should
have been written *before* the implementation — sketch-first is the
convention; this one documents a design that was iterated in chat instead.)

## Intent: what this example demonstrates

The `inspect` layer is not just read-only introspection. Combined with the
`os`/`strings`/`filepath` intrinsics, a minigo script can drive a real
codegen *orchestrator* — a tool whose output is source edits in the very
files it scanned.

`gen-sync` walks a package through the index layer, infers which
declarations want generation tooling, and rewrites a managed block of
`//go:generate` lines in each file. It is deliberately the inverse of
`convert-define`: that example reads quoted call syntax to emit *new* code;
this one scans declarations and writes edits *back into the same files*.
Together they show the two directions of "minigo touching source":
syntax → generated code, and index → generated directive kept in sync.

Three smaller things it exercises along the way:

- **Package walking** — `DirOf`/`Files`/`Decls`, plus transitive
  same-module imports via `Imports` + `PackageOf` (`-deps`).
- **The "metadata → generated directive" pattern** — a sentinel-managed
  region that a tool owns wholesale and regenerates every run. The same
  shape `nixo`/`protodesc`-style sync tools use.
- **Where the inspect view is thin** — const `ValueSpec` types are not on
  `inspect.Decl`, so enum detection drops to raw source lines at
  `inspect.Pos` coordinates. That gap is the interesting part: the script
  shows how index answers and textual answers compose.

## Key design decision: no magic comments

An early sketch had a `// @gen mock` doc marker triggering a directive.
Dropped, per feedback: if the author has to write a marker anyway, they
might as well write the `//go:generate` line itself — the tool's value is
*inference from what the code already says*. So every rule reads the
declaration's surface:

| Signal (what the code already says) | Rule | Directive |
|---|---|---|
| `type X int`/`string` + a `const` block of `X` | enum | `stringer -type=X` |
| interface named `*Service`/`*Store`/`*Client`/`*Repository` | service boundary | `mockgen -source=<file> -destination=mock_<file>` |
| struct field tag containing `required` | validation candidate | `requiredgen -type=X` |
| type declaring `Discriminator() string` | OpenAPI `oneOf` variant | `oneofgen -type=X` |

`requiredgen`/`oneofgen` are hypothetical tool names — the point is that
*commands go:generate could run* fall out of a scan, not that those tools
exist. (Side observation recorded for the follow-up pile: for simple cases
like enum `String()`, the generator itself could be minigo — no
`stringer` subprocess at all. Out of scope here.)

## Managed region: sentinel owns everything below it

Each synced file carries one sentinel line:

```go
// Code generated directives below are managed by gen-sync. DO NOT EDIT.
//go:generate stringer -type=Status
```

Every `//go:generate` line below the sentinel is tool-owned: each run wipes
them all and writes the freshly-computed expected set. This makes staleness
a non-problem — a `-type=Priority` directive for a type renamed to `Level`
disappears with no diffing logic, and an orphan directive for a deleted
type vanishes the same way. "If it gets confusing, delete all" turned out
to be the simpler correct rule, not a compromise.

Files without a sentinel gain the block right after the package clause and
imports; everything above the sentinel — user `//go:generate` lines,
package doc, build tags — is never touched. The block lives near the top
rather than per-decl so one file's directives are greppable in one place
(`go generate` does not care about position).

Idempotence falls out for free: `inspect.Doc` reads
`CommentGroup.Text()`, which already excludes `//go:generate` lines, so the
directives the tool wrote never feed back into the next scan.

## Implementation

- `main.go` — thin host: `-check` / `-deps` / positional `dir` (default
  `./app`), then `e.Run(ctx, "./script", "Main", dir, check, deps)`. The
  engine is `NewEngine(".")` with default (unrestricted) roots — the
  script writes real files, same trust model as task-run.
- `script/main.go` — `package script`, entry `Main(dir string, check,
  deps bool) int` returning the changed/drifting file count. It imports
  the real path `github.com/podhmo/minigo/inspect` (not the
  `minigo.dev/inspect` alias) so `go build`/`go vet` stay green on the
  script file itself.
- `app/` — deliberately drifted fixture: `status.go` already in sync,
  `level.go` carries a stale `-type=Priority`, `job.go` has no block, and
  `app/internal/mood` is reachable only under `-deps`.
- `testdata/*.golden` — expected post-sync files asserted by tests.

### Script-side workarounds for index gaps

- **const spec types** — `inspect.Decl` exposes `Kind`/`Name`/`File`/`Pos`/
  `Doc` but not `ValueSpec.Type`, so `hasConstOfType` reads the raw line at
  `posLine(c)` and fields-splits before `=`. Specs that omit the type walk
  back up to the `const (` block's first spec (iota-style inherited types).
- **alias vs defined type** — `type X = int` vs `type X int` is decided by
  `=` on the decl's own line (before any struct-tag backtick).
- **Discriminator** — `inspect.Methods(d)` gives method decls; name match
  on `Discriminator` is distinctive enough that the signature check
  (`inspect.Signature`) was unnecessary.

## Deliberately not done (follow-ups)

- **Per-decl placement** — directives cluster in one managed block rather
  than riding above each decl. Per-decl anchoring is possible (`Pos` is
  right there) but reintroduces the diffing the sentinel avoids.
- **Grouped `type (...)` decls** — a `type (` block containing several
  specs gets one directive set but the anchor math is per-file, not
  per-spec; not exercised by the fixture.
- **Smarter `-check` output** — it reports the expected directive count,
  not a unified diff. `difffuzz` has the diff machinery if this graduates.
- **Exclude rules** — no `//gen-sync:ignore` or path filter; a real user
  would need one before a file they hand-manage.
- **Bound packages** — `Imports` edges into host/stdlib packages aren't
  introspectable (no index); `-deps` only follows import paths under the
  scanned module prefix, which sidesteps it.
- **Ordering** — directives emit in `Decls` order (file order); stable
  because the scan is deterministic, but not sorted.

## Verification

- `go -C ./examples/gen-sync test ./...` — `TestSync` (first run: 2 files
  rewritten to goldens; second run: 0 changes), `TestCheck` (drift
  reported, nothing written; clean after a real sync), `TestDeps`
  (`internal/mood` untouched without `-deps`, synced with it).
- `make -C examples/gen-sync demo` — runs the sync twice and prints the
  `app/` diff: the before/after the README shows.

## (end)
