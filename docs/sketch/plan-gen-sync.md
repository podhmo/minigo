# plan-gen-sync.md — collecting package metadata via `inspect` to keep `//go:generate` in sync

Companion to `docs/sketch/plan-task-runner.md` and
`experiment-convert-define-on-inspect.md`. This note records what
`examples/gen-sync` is for and what it cost to build.

## Motivation

A usage of minigo that neither `examples/task-run` nor
`examples/convert-define` could express: **read package metadata through
the `inspect` API and act on it**. Two capabilities matter:

- **package walking** — enumerating decls across a package and its
  same-module import closure, and following named type references
  between decls (recursive exploration)
- **metadata collection** — gathering declarations that satisfy a
  condition out of the scanned decls' surfaces — including surfaces
  reachable through their field types and method sets

Once metadata can be collected by condition, "anything" is possible — and
many of those anythings are already done by existing tools. So the example
emits `//go:generate` directives pointing at plausible tools instead of
generating code itself. The directive lines are the artifact; whether
`requiredgen`/`oneofgen` exist doesn't matter (this is the lazy part:
writing *the go:generate statement* demos the SSoT/sync pattern — the
declaration is the single source of truth, the directive is kept in sync
with it — without paying for a real generator).

A second constraint pushed the same direction: **legacy code can't be
asked to be a pure definition**. A tool that needs the package compiled or
fully loaded puts demands on the scanned code — buildable, self-contained,
cheap to load — that real codebases don't satisfy (a `model` package with
heavy transitive deps is the norm, not the exception; a "clean seed" for
loading doesn't exist). `inspect` reads declarations from the index —
parse cost only — so scanning works on code that was never shaped to be
scanned. That tolerance, not the `//go:generate` output itself, is what a
real-world version of this pattern needs.

## Where it sits among the examples

| | `task-run` | `convert-define` | `gen-sync` |
|---|---|---|---|
| What the script *is* | a trusted build file (Taskfile) | a DSL: quoted `define.Convert` calls | a scanning tool over user code |
| Engine feature exercised | intrinsic-bound stub package (`task.*`), `os`/`exec`, virtual cwd | special forms (quoted AST args) + lazy package loading | the `inspect` API + `os` write intrinsics |
| Data direction | reads parts of a file → executes them | reads the whole script → emits new code | reads the index → edits the same files it scanned |
| Trust model | unrestricted (build scripts) | unrestricted | unrestricted (writes real sources) |

task-run proves minigo can host a tool-shaped runtime (it pulls out and
runs only the functions it was asked to); convert-define proves it can
host a codegen DSL (the whole definition file is interpreted); gen-sync
proves `inspect` alone — no special forms, no stub package — is
enough to collect metadata and put it to work.

## What gets collected, and how

Every rule infers the target from the declaration's own surface — no
marker comments (a `// @gen` marker would be the same labor as writing
`//go:generate` directly, so it buys nothing):

| Signal (what the code already says) | Rule | Directive emitted |
|---|---|---|
| `type X int`/`string` + a `const` block of `X` in the package | enum | `stringer -type=X` |
| non-alias interface named `*Service`/`*Store`/`*Client`/`*Repository` | service boundary | `mockgen -source=<file> -destination=mock_<file>` |
| struct field tag `required:"true"`, or `required` as a whole element of `validate:`/`binding:` — on the struct or any struct reachable through its field types | validation candidate, recursively | `requiredgen -type=X` |
| type declaring `Discriminator() string` | OpenAPI `oneOf` variant | `oneofgen -type=X` |
| interface requiring `Discriminator() string` | `oneOf` union; implementers collected over the walked closure | `oneofgen -type=X -variants=a,b,pkg.c` |

A decl can earn several directives or none; all `//go:generate` output is
just the collected set, deduplicated.

## Behavior: the managed region

Synced directives live under a sentinel line:

```go
// Code generated directives below are managed by gen-sync. DO NOT EDIT.
//go:generate stringer -type=Status
```

Each run rewrites the *run* of `//go:generate` lines directly under the
sentinel with the freshly collected set — stale and orphaned directives
disappear with no diffing logic, while hand-written directives survive
everywhere else: above the sentinel, or below it once a non-directive,
non-blank line breaks the run. (The earlier wipe-everything-below design
was abandoned: a hand-written directive that ends up *below* an inserted
sentinel would have been eaten — `ops.go` in the fixture pins this.)
The sentinel itself is recognized at code position only: a little
line-scanner skips `/* */` blocks and raw strings, so quoting the marker
text inside a comment or string literal does not open a managed region.
Files without a sentinel gain the block after the package clause and
imports.

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

- **Scan**: `inspect.DirOf(dir)` → `inspect.Files` → `inspect.Decls`,
  then two passes — every file's expected directives are computed before
  any write, so decl positions always refer to the pre-sync snapshot
  (writes and scans interleaved would move decls' line numbers). The
  in-subtree import closure is always walked — a BFS over
  `inspect.Imports(f)` → `inspect.PackageOf` restricted to paths under
  `inspect.Path(root) + "/"` — because it is the search space for both
  recursive rules. `-deps` only widens the *write set*: without it, only
  the scanned package's own files become sync targets. An edge that
  leaves the subtree (`app -> scanx`) is never followed or rewritten.
- **Exploration machinery** — `scanx/explore.go`: `TypeRefs` walks a
  `TypeExpr`'s composite children and collects the named leaves as
  canonical `path.Name` names (`CanonicalName` collapses local import
  aliases through the declaring file's table); `Explorer` resolves those
  names scope-gated to the subtree — `InScope`, lazy `Lookup` building
  each package's decl table at most once (a type referenced by forty
  fields costs one package read); `Reach` breadth-first follows
  references with a visited set keyed by canonical name, so cyclic type
  graphs terminate and every shared dependency is visited once. The
  `requiredgen` rule is `Reach` with an early-stop predicate; the
  `oneofgen` union rule enumerates implementers across the walked
  closure instead — cross-package variants are emitted qualified
  (`mood.Signal`).
- **`scanx`, the helper library** — the mechanics (raw-line spec reads,
  tag parsing, alias detection, sentinel/managed-region logic, method and
  interface-requirement checks) live in a sibling package
  `github.com/podhmo/minigo/examples/gen-sync/scanx` that the script
  imports like any other module-local source: the engine interprets it
  (same precedent as convert-define's `define`/`convutil`), so `go build`
  compiles the script *and* the interpreter can run it. `script/main.go`
  keeps only the policy.
- **Enum detection is native now** — the rule is
  `len(inspect.EnumMembers(d)) > 0`: the engine links every const spec
  carrying the type name (explicit `vs.Type` or the `InheritedType` an
  empty spec picks up) back to the type decl, so `Cadence Level = "4/4";
  Beat` types `Beat` as `Level` without any line reading. `type X = int`
  vs `type X int` is still decided by `=` on the decl's own line.
- **Decls are touchable** — `inspect.Def(d)` gives the type expr
  (`Kind` = ast node name: `Ident`/`StructType`/`InterfaceType`),
  `inspect.Fields(d)` the struct fields incl. tags, `inspect.Methods(d)`
  the method set (that's where `Discriminator` is found), `inspect.Pos(d)`
  the `"file:line:col"` anchor used for all textual fallback reads.
  `scanx.HasMethod`/`RequiresMethod` check signatures via
  `sig.ParamFields()`/`ResultFields()` — the `[]*Field` accessors that
  compile in real Go and unbox fine interpreted (the FFI `Params`/`Results`
  slices themselves don't `len()` outside the interpreter).
- **Insertion** — `scanx.InsertAnchor` finds the end of the package clause +
  import decls (skipping doc comments and build tags above `package`),
  and the managed block is spliced in there with one blank line on each
  side.

## Remaining work — if this were done seriously

Skipped for scope, not blocked by anything:

- **Exploration cut-off controls** — `Reach` walks the whole reachable
  subtree in one pass. A real pass wants a stop predicate: max depth, a
  boundary predicate over import paths, per-package opt-out.
- **External packages** — traversal and enumeration both end at the
  subtree; following a field type or finding an implementer in another
  module is unexplored (see limitations below for where it would break
  today).
- **Implementer completeness** — the `-variants=` collection is a
  method-set scan over the walked import closure: types that implement
  the interface only through embedding are invisible in the decl view,
  and implementers in unimported subtree packages are never enumerated
  (a dir-walk would be needed, not an import walk).
- **Per-decl placement** — `Pos` could anchor each directive above its
  decl, but that reintroduces the diffing the sentinel avoids.
- **Smarter `-check` output**, **ignore rules** (`gen-sync:ignore` /
  path filters).

## Limitations hit along the way

Gaps in the `inspect`/index surface itself that the script works around
textually — candidates for the inspect wishlist, not the example's:

- ~~**`inspect.MReqs` traps on non-interface decls**~~ — fixed:
  `MReqs`/`IEmbeds` return nil on non-interface *type* decls (non-type
  decls still trap), so `RequiresMethod` asks `MReqs` directly without
  the `Def(d).Kind == "InterfaceType"` pre-gate.
- ~~Alias vs defined type isn't on the view~~ — fixed: `inspect.IsAlias`
  reads `TypeSpec.Assign` directly; the raw-line `IsAlias`/`DeclLine`/
  `IsAliasLine`/`PosFile`/`PosLine`/`LinesOf` helpers are deleted.
- ~~**`inspect.Pos` is a `"file:line:col"` string**~~ — fixed: `Decl`,
  `Field`, and `Import` `Pos` fields plus `inspect.Pos(d)` now return
  `*inspect.Position{File, Line, Column}` (nil for host symbols, still
  `String()`-printable), so scripts read fields instead of splitting.
- ~~**Bound/stdlib/external packages carry no index**~~ — fixed:
  `Explorer.Lookup` asks `inspect.SourceOf`, which builds the source
  index behind a bound shadow (unbound paths behave as before), so
  bound packages under the subtree are introspected through their real
  decls. `BoundRef`→`bound.Marked` earns `requiredgen` while the test
  `Bind()`s the package.
- ~~**The *base* of a generic instantiation is unreachable**~~ — fixed:
  `TypeExpr.Children()` now leads an instantiation's children with the
  base (`List[Inner]` → `List` then `Inner`), so walks enter generic
  containers through instantiations of them (the fixture's `Safe`
  reaches `Vault` — and earns `requiredgen` — through the base alone).
- ~~**Tags inside anonymous struct types are unreadable**~~ — fixed:
  `inspect.TypeFields` returns a composite spelling's member elements
  (names, type, tag for structs; specs vs embeds for interfaces), the
  TypeExpr-level counterpart of `Fields`. `hasRequiredTag` descends
  into anonymous composites, so `Inline` (and `ListAnon`, a slice of
  anonymous struct) earn `requiredgen`.
- ~~**Promoted methods are invisible**~~ — fixed: `inspect.MethodSet`
  flattens the declared methods with members promoted through embedded
  fields (transitively, following the value method-set rule —
  `struct{ T }` lifts non-pointer receivers, `struct{ *T }` and
  interface embeds lift all). `EmbedEvent` joins the `-variants=` list
  and earns `oneofgen` like the compiler already accepted.
- ~~**`inspect.SymbolID` returns `runtime.NIL` for non-named exprs**~~ —
  fixed: it now returns `*runtime.SymbolID` — nil for non-named
  exprs — so `sid == nil` is both valid Go and correct under minigo.
  `TypeRefName` keys on the id's fields directly instead of parsing
  `CanonicalName`.
- **No subtype lookup** — the index is per-declaration, so "every type
  implementing I" is derived by scanning method sets across the walked
  closure; `-variants=` is that derivation.

## Verification

- `go -C ./examples/gen-sync test ./...` — `TestSync` (10 files
  rewritten to goldens across the distractor-seeded fixture; second run
  idempotent, hand-written directive in `ops.go` surviving), `TestCheck`
  (drift reported, nothing written; clean after a real sync), `TestDeps`
  (`internal/mood` untouched without `-deps`, synced with it;
  `internal/meta` visited and left alone; the `app -> scanx` edge never
  followed), plus `scanx`'s own unit tests for the mechanics.
- `make -C examples/gen-sync demo` — runs the sync twice and prints the
  `app/` diff.

## (end)
