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
a script package exposing `Overrides() map[string]any` of
`"pkgpath.Name"` -> schema fragment, consulted before the default
walk), `-stdlib leaf|walk` (default `leaf`: standard-library types
become leaf stubs instead of being walked), `-loose`
(`additionalProperties`), `-title`, `-desc`.

## Examples

```sh
go run ./ ./app User
go run ./ ./app                     # whole package -> $defs
go run ./ -plugin ./plugins/time ./app User   # time.Time -> RFC 3339 string
go run ./ -stdlib walk ./app        # walk into time.Time's source
```

## Layout

- `main.go` — flag parsing, plugin merge, engine drive, doc print.
- `script/` — the interpreted walk: decl -> schema generator.
- `app/` — fixture package (nested/anonymous/recursive/cross-package
  `internal/dep`, enum, alias, tags, embedding).
- `plugins/time/` — `Overrides()` example: `time.Time` -> `{type:
  string, format: date-time}`.
- `plugins/desc/` — `DescField`/`DescDecl` example: drops the Go doc
  convention's leading symbol name from every description. Filters are
  `func(any, string) string` script functions the walk pulls via
  `inspect.Value` and calls in the same VM; the field/decl reference is
  passed to the hook (read it via reflect — a `*inspect.Field` type
  assertion traps on cross-package type identity).
- `testdata/*.golden` — golden outputs; regenerate with
  `go test ./ -update`.

Heavier, combinatorial fixtures live in the `extract-schema/` corpus
of podhmo/minigo-usecasefuzz.
