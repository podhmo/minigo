# extract-schema

Extract a JSON Schema document for a Go package — or one named type in
it — by walking declarations through the minigo `inspect` API. A port
of [go-scan](https://github.com/podhmo/go-scan)'s `tools/genschema`
onto minigo: the scan itself is an interpreted script (`./script`), so
the package walker it exercises can be improved without rebuilding the
host.

```
extract-schema [flags] <dir|pkg> [Type]
```

- `Type` given: schema for that type. Its named-type graph lands in
  `$defs`; the root is `$ref` when the type recurs or its def is a bare
  alias reference, otherwise the def is inlined.
- `Type` omitted: whole-package mode — every type decl lands in
  `$defs` (OpenAPI `components.schemas` style), no root.

Flags: `-pkg` (target is an import path), `-plugin <dir>` (repeatable;
a script package whose exported `func(*extschema.Config)` shapes are the entry points — see
Plugins below), `-stdlib leaf|walk` (default `leaf`: standard-library types
become leaf stubs instead of being walked), `-loose`
(`additionalProperties`), `-title`, `-desc`.

## Examples

```sh
go run ./ ./app User
go run ./ ./app                     # whole package -> $defs
go run ./ -plugin ./plugins/time ./app User   # time.Time -> RFC 3339 string
go run ./ -plugin ./plugins/desc ./app Commented   # desc filters
go run ./ -stdlib walk ./app        # walk into time.Time's source
```

## Plugins

`-plugin <dir>` (repeatable) names a script package; its entry point
is any exported `func(c *extschema.Config)` — detected by signature
shape, not name. The shared registrar lives in `extschema/` (the same
role gen-sync's `scanx/` plays). The walk calls each matching func
inside the same VM:

- `c.Override(symbolID, fragment)` — `symbolID -> schema fragment`
  consulted before the default named-type walk (`time.Time` -> RFC
  3339 in `plugins/time`).
- `c.DescField(fn)` / `c.DescDecl(fn)` — `func(any, string) string`
  filters on every emitted description; the field/decl reference is
  passed (narrowed via reflect — a `*inspect.Field` type assertion
  traps on cross-package type identity; see `plugins/desc`).

## Layout

- `main.go` — flag parsing, engine drive, doc print.
- `script/` — the interpreted walk: decl -> schema generator.
- `extschema/` — shared plugin contract (`Config` registrar).
- `app/` — fixture package (nested/anonymous/recursive/cross-package
  `internal/dep`, enum, alias, tags, embedding).
- `plugins/time/` — overrides example: `time.Time` -> `{type: string,
  format: date-time}`.
- `plugins/desc/` — desc-filter example: drops the Go doc
  convention's leading symbol name from every description.
- `testdata/*.golden` — golden outputs; regenerate with
  `go test ./ -update`.

Heavier, combinatorial fixtures live in the `extract-schema/` corpus
of podhmo/minigo-usecasefuzz.
