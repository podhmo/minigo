# plan-gen-sync.md — collecting package metadata to keep `//go:generate` in sync

Companion to `docs/sketch/plan-task-runner.md` and
`experiment-convert-define-on-inspect.md`. This note records what
`examples/gen-sync` is for and what it cost to build.

## Motivation

A usage of minigo that neither `examples/task-run` nor
`examples/convert-define` could express: **read package metadata out of
the index layer and act on it**. Two capabilities matter:

- **package walking** — enumerating decls across a package, and (with
  `-deps`) across its same-module import closure
- **metadata collection** — gathering declarations that satisfy a
  condition out of the scanned decls' surfaces

Once metadata can be collected by condition, "anything" is possible — and
many of those anythings are already done by existing tools. So the example
emits `//go:generate` directives pointing at plausible tools instead of
generating code itself. The directive lines are the artifact; whether
`requiredgen`/`oneofgen` exist doesn't matter (this is the lazy part:
writing *the go:generate statement* demos the SSoT/sync pattern — the
declaration is the single source of truth, the directive is kept in sync
with it — without paying for a real generator).

## Where it sits among the examples

| | `task-run` | `convert-define` | `gen-sync` |
|---|---|---|---|
| What the script *is* | a trusted build file (Taskfile) | a DSL: quoted `define.Convert` calls | a scanning tool over user code |
| Engine feature exercised | intrinsic-bound stub package (`task.*`), `os`/`exec`, virtual cwd | special forms (quoted AST args) + lazy package loading | the `inspect` index layer + `os` write intrinsics |
| Data direction | reads parts of a file → executes them | reads the whole script → emits new code | reads the index → edits the same files it scanned |
| Trust model | unrestricted (build scripts) | unrestricted | unrestricted (writes real sources) |

task-run proves minigo can host a tool-shaped runtime (it pulls out and
runs only the functions it was asked to); convert-define proves it can
host a codegen DSL (the whole definition file is interpreted); gen-sync
proves the index layer alone — no special forms, no stub package — is
enough to collect metadata and put it to work.

## What gets collected, and how

Every rule infers the target from the declaration's own surface — no
marker comments (a `// @gen` marker would be the same labor as writing
`//go:generate` directly, so it buys nothing):

| Signal (what the code already says) | Rule | Directive emitted |
|---|---|---|
| `type X int`/`string` + a `const` block of `X` in the same file | enum | `stringer -type=X` |
| interface named `*Service`/`*Store`/`*Client`/`*Repository` | service boundary | `mockgen -source=<file> -destination=mock_<file>` |
| struct field tag containing `required` | validation candidate | `requiredgen -type=X` |
| type declaring a `Discriminator() string` method | OpenAPI `oneOf` variant | `oneofgen -type=X` |

A decl can earn several directives or none; all `//go:generate` output is
just the collected set, deduplicated.

## Behavior: the managed region

Synced directives live under a sentinel line:

```go
// Code generated directives below are managed by gen-sync. DO NOT EDIT.
//go:generate stringer -type=Status
```

Each run wipes every `//go:generate` below the sentinel and writes the
freshly collected set — stale and orphaned directives disappear with no
diffing logic. The sentinel is honestly a safeguard, not the point: it
keeps the tool from destroying `//go:generate` lines the user wrote by
hand (everything above it is untouched), and it's what makes "regenerate
from scratch each run" safe. Files without a sentinel gain the block
after the package clause and imports.

Idempotence is free: `inspect.Doc` reads `CommentGroup.Text()`, which
excludes `//go:generate` lines, so the tool's own output never feeds back
into the next scan.

## Implementation notes — where the interesting parts live

`main.go` is a thin host: `-check` / `-deps` / positional `dir` (default
`./app`), then `e.Run(ctx, "./script", "Main", dir, check, deps)` on a
`NewEngine(".")` (unrestricted — the script writes real files). The
script is `script/main.go`, `package script`, entry
`Main(dir string, check, deps bool) int` returning the changed/drifting
file count. It imports the real path `github.com/podhmo/minigo/inspect`
rather than `minigo.dev/inspect` so `go build`/`go vet` stay green on the
script itself; the engine binds both paths to the same intrinsics.

- **Scan**: `inspect.DirOf(dir)` → `inspect.Files` → `inspect.Decls`.
  Under `-deps`, a BFS over `inspect.Imports(f)` → `inspect.PackageOf`
  follows only paths under the module prefix (`filepath.Dir(Path(root)) +
  "/"`), so stdlib and bound packages are never entered.
- **Where the index is thin** — `inspect.Decl` gives `Kind`/`Name`/`File`/
  `Pos`/`Doc` but not `ValueSpec.Type`, so enum detection (`hasConstOfType`)
  reads the raw source line at `posLine(c)` and splits fields before `=`;
  type-omitted specs walk back up to the `const (` block's first spec
  (iota inheritance). `type X = int` vs `type X int` is decided by `=` on
  the decl's own line.
- **Decls are touchable** — `inspect.Def(d)` gives the type expr
  (`Kind` = ast node name: `Ident`/`StructType`/`InterfaceType`),
  `inspect.Fields(d)` the struct fields incl. tags, `inspect.Methods(d)`
  the method set (that's where `Discriminator` is found), `inspect.Pos(d)`
  the `"file:line:col"` anchor used for all textual fallback reads.
- **Insertion** — `insertAnchor` finds the end of the package clause +
  import decls (skipping doc comments and build tags above `package`),
  and the managed block is spliced in there with one blank line on each
  side.

## Remaining work — if this were done seriously

Skipped for scope, not blocked by anything:

- **Recursive exploration** — every rule reads only the decl's own
  surface. A serious version would chase field types and method sets
  through `TypeExpr` (`Sub`/`Resolve`) into other specs and packages:
  a field whose type itself carries `required`, or collecting every
  implementer of the `Envelope` interface instead of checking
  `Discriminator` per type.
- **Exploration cut-off controls** — `-deps` is a flat BFS gated only by
  the module prefix. A real pass wants a stop predicate: max depth, a
  boundary predicate over import paths, per-package opt-out.
- **External packages** — traversal ends at the module prefix; following
  a field type into another module is unexplored (see limitations below
  for where it would break today).
- **Grouped `type (...)` decls** — honest laziness: the index emits one
  `Decl` per `TypeSpec` with its own `Pos`, and the managed block is
  per-file anyway, so groups should already collect correctly — the
  fixture just doesn't cover it.
- **Per-decl placement** — `Pos` could anchor each directive above its
  decl, but that reintroduces the diffing the sentinel avoids.
- **Signature-precise rules** — `Discriminator` matches on the method
  name; `inspect.Signature` exists for exact signatures.
- **Real tag parsing** — `strings.Contains(tag, "required")` is textual.
- **Smarter `-check` output**, **ignore rules** (`gen-sync:ignore` /
  path filters), **sorted output** — emits in `Decls` order today.

## Limitations hit along the way

Gaps in the `inspect`/index surface itself that the script works around
textually — candidates for the inspect wishlist, not the example's:

- **`inspect.Decl` hides `ValueSpec.Type`/`InheritedType`** — a const
  spec's declared type is invisible, so the enum rule reads raw source
  lines at `posLine` coordinates and re-derives iota inheritance by
  walking up to `const (`.
- **Alias vs defined type isn't on the view** — `type X = int` vs
  `type X int` is decided by `=` on the decl's own line.
- **`inspect.Pos` is a `"file:line:col"` string** — line numbers come
  from splitting it; a structured accessor would remove the parse.
- **Bound/stdlib/external packages carry no index** — `Decls`/`Fields`/
  `Methods` can't introspect them at all; `-deps` only works because the
  module-prefix gate never enters them.
- **No subtype lookup** — the index is per-declaration, so "every type
  implementing I" has to be derived by scanning method sets.

## Verification

- `go -C ./examples/gen-sync test ./...` — `TestSync` (2 files rewritten
  to goldens; second run idempotent), `TestCheck` (drift reported,
  nothing written; clean after a real sync), `TestDeps` (`internal/mood`
  untouched without `-deps`, synced with it).
- `make -C examples/gen-sync demo` — runs the sync twice and prints the
  `app/` diff.

## (end)
