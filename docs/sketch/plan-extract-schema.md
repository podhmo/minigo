# plan-extract-schema.md — JSON Schema extraction over the inspect API

An experiment note for `examples/extract-schema`: a CLI that takes a package
(a directory or an import path) plus an optional type name, walks the
struct graph, and prints a JSON Schema. It is a port of go-scan's
`tools/extract-schema` onto minigo's `inspect` API — with one deliberate
difference: the walk is a *script*, not host Go.

## Motivation

The reference implementation, `tools/extract-schema` in go-scan, generates a
draft-07 JSON Schema from a `--query <pkg>.<Type>` by walking
`scanner.TypeInfo` host-side: structs become `object` schemas, named
types become `$defs` entries joined by `$ref`, circular references
terminate through a `seen` map, and struct tags drive property names,
`required`, and per-field overrides.

Re-implementing that walk on minigo is not about needing another schema
generator — it is a coverage driver. A real schema walk exercises
nearly the whole `inspect` surface in one pass:

- **Decl navigation** — `PackageOf`/`DirOf`, `Decls`, `Symbol`, `Kind`,
  `Doc`, `IsAlias`, `EnumMembers`.
- **Type-expression navigation** — `Def`, `TypeExpr.Kind`, `Children`,
  `UnRef`, `UnWrap`, `SymbolID`, `Resolve`, `TypeFields` (anonymous
  structs), `CanonicalName`.
- **Package metadata** — `Standard` (the early-stop signal), `Path`,
  `Name`, `State`.
- **Value layer (sparingly)** — `Value` for enum member constants.

It also lines up with two TODO.md items this tool wants as inputs:
`modulewalk` (repo-enumeration surface: "all packages under a root",
which a whole-repo schema run would consume) and
`syntax.ParseImports`/`ParsePackageClause` (cheap scans). The example
does not implement them; it defines the consumer they would feed.

### The experiment's acceptance list

The point of the exercise is whether the following hold, end to end:

- **inspect-driven coverage** — everything the generator needs must
  come from `inspect` views; dropping to `go/parser` host-side for
  shape questions would be the finding.
- **Nested analysis that actually nests** — inner structs, anonymous
  struct fields, and *recursive* definitions (`type Node struct { Next
  *Node }`) produce `$defs`/`$ref` rather than infinite descent.
- **Indirect cross-package following** — a field typed
  `otherpkg.T` resolves into that package and keeps walking; a
  second-order import (a package the target package imports but the
  entry package does not) must still be reachable.
- **Stopping midway** — stdlib types like `time.Time` are leaves by
  default; nobody wants a schema describing `time.Location.zone`. The
  cutoff must be a policy, not a hardcoded name list.
- **Installable interpretation** — the script's reading of a Go type
  is extensible by dropping in a *plugin* (itself a script), e.g.
  `time.Time` → `{"type":"string","format":"date-time"}`.

## What it does

```console
$ extract-schema ./app User          # directory form
$ extract-schema -pkg example.com/m/app User   # import-path form
{ "type": "object", "properties": { ... }, "$defs": { ... } }
```

`extract-schema <target> [Type]` prints one JSON Schema document to stdout:

- **Type mode** — `extract-schema ./app User`: the root type is inlined
  when it is used only once and is not a bare `$ref`; otherwise it
  stays `{"$ref": "#/$defs/<Type>"}`, matching the reference tool's
  assembly.
- **Package mode** — `extract-schema ./app`: every type decl in the
  package lands in `$defs`, OpenAPI `components.schemas` style, with no
  root. The whole-package document is deliberately just a `$defs`
  table — a registry, not an instance schema.

### Type mapping (mirroring `tools/extract-schema`)

| Go shape | inspect surface | Schema |
|---|---|---|
| `struct{...}` (named or anonymous) | `Fields` / `TypeFields` | `object` + `properties` + `required`; unexported skipped |
| `type T = U` (alias) | `IsAlias`, `UnWrap`/`Resolve` | `{"$ref": "#/$defs/U"}`-style alias def |
| `type T ...` (named) | `SymbolID` → `Resolve` | `$defs` entry + `{"$ref": ...}`; `seen` map breaks cycles |
| `[]E`, `[N]E` | `Kind=="ArrayType"`, `Children[0]` | `array` + `items` |
| `map[K]V` | `Kind=="MapType"`, `Children[1]` | `object` + `additionalProperties` |
| `*T` | `Kind=="StarExpr"`, `UnRef` | transparent; marks the field optional |
| `interface{}`/`any` | `Kind=="InterfaceType"`/`Ident` | `{"type":"object","additionalProperties":true}` |
| builtins | `Kind=="Ident"`, `SymbolID==":builtin:"` | `boolean`/`integer`/`number`/`string` |
| enum types | `EnumMembers` + `Value` | base schema + `enum: [...]` |
| field tags | `Field.Tag` + `reflect.StructTag` | property name from `json`/`yaml`/`toml`; `omitempty` and `required` drive `required`; `jsonschema-override` merges literal keys |

Field doc comments become `description`, on both properties and
`$defs` entries.

## Design notes and deviations from the reference

- **The walk is a script.** `script/` is interpreted Go; the host
  `main.go` only parses flags, runs plugins, runs the main script, and
  prints. This is the same shape as `gen-sync` (host drives,
  `script/` + `scanx/` do the work) and keeps the experiment honest:
  gaps land on the interpreter, not on the porting author's host-side
  shortcuts.
- **Cross-package traversal is one call.** `inspect.PackageOf(path)`
  + `inspect.Symbol(pkg, name)` loads the decl's package on demand
  (`loadPath` — parse+index, no init, no compile). Following
  `otherpkg.T` costs exactly that package. The laziness contrast is
  the point of doing this on minigo: go-scan's `scanner` resolves
  lazily too, but there the walk is host Go over `TypeInfo`; here the
  *same walk* is the user-visible artifact.
- **`seen` is keyed by `SymbolID`**, not by a `*TypeInfo` pointer —
  identity survives across packages, so a recursive type in another
  package still terminates. `CanonicalName()` is the fallback spelling
  when `SymbolID` reports nil (generic instantiations).
- **Cutoff is a policy hook, not a name list.** A decl whose
  `inspect.Standard(pkg)` reports true produces a `{"description":
  "stdlib type <pkg>.<Name> (walk stopped)"}` leaf instead of a walk —
  unless a plugin or `-stdlib=walk` says otherwise. The check runs on
  the *package*, before symbol resolution, so bound stdlib packages
  (whose member decls do not exist) are never resolved at all. This is
  new vs the reference (which happily scans the stdlib).
- **Output order.** The reference uses `orderedmap` and hand-sorts
  `description` first. The script emits JSON through its own emitter;
  map keys come out sorted (deterministic, diff-friendly), not
  declaration-ordered — a documented deviation.
- **Enum values via `inspect.Value`.** go-scan precomputes
  `member.ConstVal`; inspect exposes the member *decls*, and `Value`
  materializes each constant lazily — no package init needed for
  consts.

## Plugins — installable interpretation

A plugin is a directory holding a minigo script package — the same
unit the tool itself is written in, so "installing" means pointing at
a directory, not compiling anything:

```console
$ extract-schema -plugin ./plugins/time -plugin ./plugins/uuid ./app User
```

A plugin's entry point is **any exported func whose signature is
`func(c *extschema.Config)`** — detected by *shape*, not name
(`plugins/time` calls it `Install`, `plugins/desc` calls it
`Configure`; both work). `extschema` is a small shared package (the
counterpart of gen-sync's `scanx/`) holding the registrar type:

```go
// plugins/time/schema.go — package timeschema
func Install(c *extschema.Config) {
    c.Override("time.Time", map[string]any{"type": "string", "format": "date-time"})
}
```

The walk loops the plugin dirs, scans `inspect.Decls` for funcs with
no receiver and one param resolving (via `UnRef` + `Resolve`) to
`extschema.Config`, pulls each out with `inspect.Value`, asserts
`func(*extschema.Config)`, and calls it — a normal in-VM call, no
respawn, no host-side merge. The registrar is a *named* API surface:
no untyped map, no magic keys, and room for the higher grades
(naming/predicate hooks) as more methods.

Current methods:

- `c.Override(symbolID, fragment)` — `symbolID` (`"pkgpath.Name"`) ->
  schema fragment consulted before the default named-type handling —
  including before the stdlib leaf policy, which is how `time.Time`
  becomes `date-time` instead of a stopped leaf.
- `c.DescField(fn)` / `c.DescDecl(fn)` — `func(any, string) string`
  filters run on every field/type description. The field/decl
  *reference* is passed, so a filter can read Names, Tag, or Pos
  instead of re-deriving the symbol from the text.

Why `func(*extschema.Config)` asserts cleanly while
`func(*inspect.Field)` does not: source packages are interned — the
plugin's `import ".../extschema"` resolves to the same package
instance as the script's, so the named type is identical. The
`inspect` stub types are not interned (nor registered as type
symbols), so view types can only cross as `any` + `reflect`
(`reflect.ValueOf(f).Elem().FieldByName("Names")` — see
`plugins/desc`).

Function-valued hooks are why "filters" is a function-shaped contract
and not data: a first cut carried rules as strings
(`"strip-symbol-prefix"`, `"replace:o->n"`) because structured values
(`[]map[string]any`) hit the arg-boundary boxing/spelling traps — but
functions sidestep all of it, and arbitrary filters (symbol-prefix
stripping being just the example in `plugins/desc`) need real code
anyway. See `plugins/time` (overrides) and `plugins/desc` (desc
filters).

### Interface shape — decided

The contract is *shape*-based: any exported `func(c *extschema.Config)`
is an entry point, detected via `inspect.Signature(d)` +
`UnRef` + `Resolve` — no blessed name at all. `extschema/` is the
shared stub package (the same answer `scanx/` gives gen-sync). It
works because source-package imports are interned across
`inspect.DirOf`-loaded plugin roots; the remaining rough edge is only
that `*inspect.Field`/`Decl` can't be named in signatures (they're not
interned/registered — hooks take `any` and narrow via reflect).

### Rejected alternatives — the contract's history

Each earlier shape was rejected (or absorbed); the reasons are the
design record.

- **Data-return `Overrides() map[string]any`** — the first cut: the
  plugin *returns* a static table and the host merges it. Rejected in
  favour of *configuring*: returning data gave the caller nothing to
  hang hooks on (grade B/C need callbacks, not fragments), and each
  return needed host-side deep-unwrapping. It survives *inside* the
  registrar as `c.Override(symbolID, fragment)` — the static-table
  grade A is still all most plugins need.
- **String-spelled rules `DescRules() []string`** — kept filters as
  data (`"strip-symbol-prefix"`, `"replace:o->n"`) after structured
  tables (`[]map[string]any`) hit the arg-boundary boxing/spelling
  traps. Rejected: an interpreter can call *functions* — string
  incantations were a workaround-shaped API, and filters genuinely
  need code plus the field/decl *reference* (Names, Tag, Pos), which
  text rules could never carry. The premise that drove it — "you can't
  pass structured/higher-order values across the boundary" — was
  half-wrong: values fail, *functions* pass and call fine in-VM.
- **Unified `Config() map[string]any`** — folded overrides and hooks
  into one returned map so the walk became a plain loop. Rejected:
  an untyped map with required magic keys is user-hostile — no
  signature to discover, nothing to document, typos silently ignored.
  It also rested on the premise that *typed* cross-package contracts
  would trap the way `func(*inspect.Field)` did; interning of source
  packages disproved that for non-stub types, removing the only
  reason to stay untyped.
- **Blessed name `Configure(c *extschema.Config)`** — the registrar
  itself stayed, but name-detection was dropped for shape-detection:
  a fixed name is an arbitrary coupling (why `Configure` and not
  `Install`? — `plugins/time` now uses the latter to prove it), while
  the signature already says everything the loader needs. Rejected
  in favour of scanning `Decls` for the `func(*extschema.Config)`
  shape — several entry funcs may coexist, and none needs a
  conventional name.

One premise worth recording on its own: early on, a typed contract was
assumed impossible *by analogy* — `func(*inspect.Field)` asserts fail
across plugin roots, so it was inferred that all named param types
would. That analogy held for `inspect` stub views only; source
packages are interned, so `*extschema.Config` — precisely the type
plugins need — asserts cleanly. The untyped-map detours existed
because of this wrong premise; correcting it is what let the contract
become typed and nameless.

Extension grades, in order; the spike builds the first:

- **A. Static table** (above) — data-only plugins, no callbacks. Built.
- **B. Predicate hook** — a plugin exports
  `SchemaFor(d *inspect.Decl) map[string]any` (nil = decline), letting a
  plugin match on *shape* — "any type with a `MarshalJSON` method is a
  string" — rather than identity. This exercises calling a function
  value from one script package inside another's run.
- **C. Field-naming policy** — a `FieldName(f *inspect.Field) string`
  hook choosing the property spelling: `json` tag name (default),
  `kebab-case`, the raw field name, ... . Shape B and C compose:
  `Overrides` still wins on identity.

## Verification

- **Local (this example):** a small fixture module (`app/`) covering
  the core shapes — nested/anonymous structs, recursion, a cross-package
  field, an enum, aliases, tags — asserted as goldens with go-cmp, in
  the `setupModule` style `gen-sync` established. Deliberately
  *exhaustive-per-shape but small*.
- **Corpus (minigo-usecasefuzz):** a `extract-schema/` corpus dir for the
  messy matrix — generics, embedded fields, third-party imports,
  real-world structs — run ad hoc, verdicts committed there.
- **Findings loop:** gaps the walk hits in `inspect` (or in the
  interpreter, running the script) become TODO.md entries; the
  "Asks of minigo" list below is the running ledger.

## Steps

1. **Scaffold** — module, host flags, script skeleton: `DirOf`/`Symbol`
   → `Fields` → scalar/`StructType`/`ArrayType`/`MapType`/`StarExpr`
   mapping for one package.
2. **Named-type graph** — `SymbolID`/`Resolve` recursion across
   packages, `seen` → `$defs`/`$ref`, alias, enum, anonymous structs
   via `TypeFields`.
3. **Cutoff** — `Standard()` leaf policy + `-stdlib=walk|leaf` flag.
4. **Plugins** — `-plugin` loading, merged overrides, the
   `plugins/time` example; predicate-hook grade if the spike allows.
5. **Corpus + report** — usecasefuzz fixtures; fold findings into the
   "Spike feedback" section and TODO.md.

## Is this on-theme for minigo?

Yes — more squarely than `test-detect` was. The tool *is* a script;
its host is 100 lines of flags and plumbing. It is the canonical
"Go-source-as-data, lazily" workload: every package the walk loads is
one it actually needed, and the stdlib cutoff keeps it from paying for
`time.zone` to answer "what does `User` look like". If the walk ever
has to say "this needs go/types", that is the experiment failing
usefully — it names the next inspect feature.

## Spike feedback — what the prototype verified

Built and passing locally (`go run ./ ./app User` and friends):

- **Verified working end to end**: single-type mode (recursive `User`
  self-references terminate through `seen` → `$ref`), whole-package
  mode (`$defs` registry), nested/anonymous structs via `TypeFields`,
  cross-package follow into `internal/dep`, enum member values via
  `inspect.Value`, alias defs, `json`/`yaml`/`toml` tag names,
  `omitempty`/pointer/`required` tag → `required`, `jsonschema-override`
  merges, embedded-field flattening (JSON's promoted-fields rule, with
  a cycle guard that degrades recursive embeds to a named `$ref`),
  unexported fields and `json:"-"` skipped, the `plugins/time`
  override, and `-stdlib leaf|walk` both ways.
- **Bound packages are the cutoff's friend.** `inspect.PackageOf` on a
  bound path (e.g. `time`) returns the host package — `Standard()`
  reports true and `Symbol()` answers host pseudo-decls
  (`Kind=="host"`). `leaf` mode stops *before* any decl lookup, so no
  GOROOT parse ever happens. In `walk` mode the script instead calls
  `inspect.SourceOf`, which builds the source index behind the bound
  shadow — the same pattern gen-sync's `scanx.Explorer` uses.
- **Verified working**: `inspect.Path/Name/Standard/Symbol/Files/Decls/
  Kind/Doc/IsAlias/EnumMembers/Value/Def/TypeFields/SymbolID/UnRef/
  Children`, `reflect.StructTag` in script, `fmt`, `sort`, `strings`,
  `errors`, `os.Stderr`.

### Interpreter/API gaps the walk hit (findings)

- **`return nil` on a `*runtime.Package`-typed helper traps**:
  `cannot use *runtime.Package as *runtime.Package` — the package type
  imported by a script is a *different type identity* than the engine's
  intrinsic `runtime.Package`. Workaround: keep package values in
  `:=`-inferred slots, never declare the type. gen-sync never needed
  the annotation, which is why it did not hit this. *Resolved in
  #875:* the `runtime` bind carries a `Package` host type and the
  pointer-type checks compare HostNew-native types, so declared
  `*runtime.Package` slots accept the intrinsic's value — the
  workaround is no longer needed.
- **Multi-return scripts arrive as `*runtime.Tuple`**, not a
  destructured pair — the host must index `Elems` (gen-sync documents
  the same shape; worth a shared host helper). *Resolved in #877:*
  `runtime.DeepUnwrap` unwraps Tuple/Map/GoValue/UConst recursively
  and `runtime.ErrorOf` decodes the `(T, error)` slot; both
  `unwrapDeep` copies and task-run's `resultErr` were deleted. Cycle-
  and key-safety followed in #888/#889.
- **`map[string]any` crosses the boundary as `*runtime.Map`** — and
  nested values stay boxed. The current design sidesteps the host-side
  `unwrapDeep` entirely: plugin `Config()` is consumed *inside* the
  script's VM, so the map never crosses the boundary. *Resolved in
  #877:* `runtime.DeepUnwrap` expands maps to `map[any]any` (they
  re-box through `SnapshotOf`, so values round-trip); array-keyed
  maps keep comparable keys since #889.
- **`inspect.TypeFields` (intrinsic) is errorless**, unlike its
  `TypeFieldsOf` host twin — non-struct kinds return empty rather than
  an error, so the walk checks `Kind` first. Naming the error-returning
  one `Of` while the intrinsic is bare is consistent with
  `Fields`/`FieldsOf`, but reads subtly different in script code.
  *Resolved in #872:* the asymmetry had already been fixed before the
  stack — both sides error on non-composite kinds today, and the
  contract is pinned by the `TypeFieldsError` tests.
- **Doc comments carry their trailing newline** — `d.Doc` /
  `f.Doc` end with `\n`; emitters may want to trim (the script now
  `TrimSpace`s every description). *Resolved in #872:* `Decl.Doc` and
  `Field.Doc` return the text without the trailing newline.
- **Comment-position coverage verified**: `inspect.Field.Doc` already
  merges both positions — a doc comment above the field wins, and the
  trailing same-line comment is used when there is no doc group
  (`fieldDoc`: `fd.Doc` preferred, `fd.Comment` fallback). Fixture
  `Commented` pins all three shapes.
- **The `[]map[string]any` signature trap**: a script param spelled
  `[]map[string]any` rejects the arriving `[]map[string]interface {}`
  (and `interface{}` spelling flips the space: `interface {}` vs
  `interface{}` — the boundary check is spelling-based). Only
  `[]string`/`[]byte`-shaped element types auto-unbox reliably;
  `[]any` elements arrive `*runtime.GoValue`-boxed and unindexable.
  Function hooks sidestep all three (below). *Resolved in #878:* the
  param check mints composite element types structurally
  (`anonExprOf`), so `any`/`interface{}`/`interface {}` spellings
  converge to one, and anonymous slice/array arguments deep-unbox
  element-wise — `rs[0]["k"]` works. The len-0 subslice aliasing
  regression in that guard was fixed in #890.
- **`inspect.Value(pkg, name)` returns a live function value** for a
  func decl, callable in the same VM — the plugin hook mechanism.
- **Source-package imports ARE interned; the `inspect` stub types are
  NOT** — the finding behind the registrar design. A plugin's
  `import ".../extschema"` resolves to the same package instance as
  the script's, so `.(func(*extschema.Config))` asserts cleanly and a
  typed plugin contract works end to end. But `.(func(*inspect.Field,
  string) string)` traps "interface conversion: ... types from
  different packages" — each separately-loaded package instance
  carries distinct inspect type objects (the same family as the
  `*runtime.Package` identity trap), and `x.(*inspect.Field)` inside
  a plugin traps `undefined: inspect.Field` — the view types are not
  registered type symbols at all. Result: hooks take `any` and
  narrow via `reflect` (`FieldByName`), while plugin *contracts* can
  name shared source-package types directly. *Resolved in #874:* the
  ten view types register via `hostType`, so `x.(*inspect.Field)` and
  `func(*inspect.Field, string) string` hooks name a real identity.
  Residual: `inspect.Resolver` stays unregistered and constructing a
  view struct in script still mints the host zero value — documented
  in the PR body.
- **`inspect.Symbol(pkg, name)` traps on a miss** — there is no
  non-trapping name lookup, so optional plugin entry points are
  probed by scanning `inspect.Decls` (`findFunc`). A `Lookup`-
  style soft variant would read better. *Resolved in #865/#866:*
  `inspect.LookupSymbol` answers nil on a miss, and `LookupDirOf` /
  `LookupPackageOf` do the same for the loaders (a broken graph still
  surfaces as an error — `locator.ErrBrokenConfig`, #882). The
  findFunc scan is gone.

## Asks of minigo

- A repo-enumeration surface for "walk every package under a root" —
  the `modulewalk` TODO item this tool would consume for a whole-repo
  mode.
- ~~The `*runtime.Package` type-identity trap above — either unify the
  identities or make the error message name the mismatch.~~ *Resolved
  in #875* (see the finding above).
- ~~Register the inspect view types (`*inspect.Field`, `*inspect.Decl`,
  ...) as real type symbols and intern them across package instances,
  so plugin hook signatures can name them directly instead of
  `any`+reflect. Same goes for a non-trapping `inspect.Lookup` —
  optional entry points currently require a Decls scan.~~ *Resolved
  in #874 / #865–#866* (see the findings above).
- ~~A caller-visible way to pick a package's loading path (bound stub
  vs source index) — a load option or two split load functions —
  because today the path is chosen invisibly and the behavioral
  differences (interning, `Standard()`, bound-shadow misses)
  leak through it.~~ *Resolved in #876:* `inspect.BoundOf` answers the
  bound-only direction and the stub comments spell which loader
  answers which object (`DirOf`/`LookupDirOf` are already
  source-only).
- Whatever the usecasefuzz corpus hits next: generic instantiations
  (`IndexExpr` walks the base; type arguments are not substituted —
  the same boundary convert-define documents), `FuncType`/`ChanType`
  fields currently error loudly. *Partly resolved:* `SymbolID` marks
  type parameters with the `:typeparam:` pseudo path (#873) so the
  walk no longer traps on generic decls (corpus c06/c07/c11 green);
  `func`/`chan`-typed fields still error loudly by design — the
  corpus pins them as FAIL.
