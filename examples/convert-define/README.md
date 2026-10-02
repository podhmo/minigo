# Go Type Converter (`examples/convert-define`)

This directory contains `convert-define`, a command-line tool that automatically generates Go type conversion functions. The definition file runs inside the `minigo` interpreter itself: `define.Convert`/`define.Rule` are registered special forms, so their arguments arrive as quoted AST. Type information comes from minigo's own lazy package loading (`engine.Package`) viewed through the `inspect` layer (`inspect/inspect.go`) — the vendored copy of `go-scan` this tool used to ship under `pkg/` is gone.

This tool provides a modern, IDE-friendly way to define conversions, replacing the older annotation-based approach.

## Overview: The `define` API

In many Go applications, you need to convert data between different struct types, such as:
*   Converting from a database model to an API response model (DTO).
*   Transforming data from an external service's format to an internal application format.
*   Mapping between different versions of a data structure.

Manually writing these conversion functions is tedious and error-prone. This tool automates the process by letting you define conversion rules in a way that is **statically valid Go code**. This means you get full IDE support, including auto-complete and type checking, which is a significant improvement over using struct tags and magic comments.

The core principle is to define **exceptions and custom logic**. By default, the generator will automatically map all fields with matching names. The `define` API is used to override this default behavior.

## Getting Started

### 1. Create a Definition File

Create a Go file to define your conversion rules (e.g., `definitions.go`). This file will use the `define` API. It should have a `//go:build codegen` tag to ensure it's not included in your main application build.

Here is an example `definitions.go`:

```go
//go:build codegen
// +build codegen

package main

import (
	"github.com/podhmo/minigo/examples/convert-define/convutil"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/destination"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/funcs"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/source"

	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	// Define global rules for types that cannot be mapped automatically.
	define.Rule(convutil.TimeToString)
	define.Rule(convutil.PtrTimeToString)

	// Define the conversion from SrcUser to DstUser, only specifying the exceptions.
	// The source and destination types are inferred from the function signature.
	// Fields with matching names (e.g., Details, CreatedAt) are mapped automatically.
	define.Convert(func(c *define.Config, dst *destination.DstUser, src *source.SrcUser) {
		// Exception 1: Different names.
		c.Map(dst.UserID, src.ID)

		// Exception 2: Different names AND a custom conversion function.
		c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)

		// Exception 3: A computed field.
		c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))
	})

	// Define conversion for a nested struct with name differences.
	define.Convert(func(c *define.Config, dst *destination.DstAddress, src *source.SrcAddress) {
		c.Map(dst.FullStreet, src.Street)
		c.Map(dst.CityName, src.City)
	})
}
```

### 2. Run the Generator

Execute the `convert-define` tool from your terminal, pointing it to your definition file.

```bash
go run github.com/podhmo/minigo/examples/convert-define -file definitions.go -output generated.go
```

### 3. Use the Generated Code

The tool will create `generated.go` (or your specified output file) containing the conversion functions. For a source type `SrcUser` and destination `DstUser`, the tool will generate:

*   `func ConvertSrcUserToDstUser(ctx context.Context, src *source.SrcUser) (*destination.DstUser, error)`

You can then call this function directly in your application code.

## The `define` API Reference

The public API is housed in the `github.com/podhmo/minigo/examples/convert-define/define` package. `Convert`/`Rule` are generic functions, and on go1.27+ toolchains `c.Convert`/`c.Compute` are generic methods — the go1.26 module keeps both variants buildable by splitting the method declarations by `//go:build go1.27` (a file-level release constraint also raises that file's language version, so generic methods compile in a `go 1.26` module).

*   `define.Convert(mapFunc)`: Defines a conversion between two struct types. The source and destination types are inferred from the signature of the mapping function, which must be `func(c *Config, dst *DstType, src *SrcType)`. Generic — `Convert[Dst, Src]` — so the mapFunc shape is checked statically.
*   `define.Rule(customFunc)`: Defines a global, reusable conversion rule for a specific type-to-type conversion (e.g., `time.Time` to `string`). Generic — `Rule[Src, Dst]` — so the customFunc must have signature `func(context.Context, *model.ErrorCollector, Src) Dst`.
*   `c.Map(dstField, srcField)`: Maps a source field to a destination field with a **different name**. Stays `any`-typed: mapped pairs may differ in type and convert through registered rules (e.g. `[]SrcItem` -> `[]DstItem`), which no signature can express.
*   `c.Convert(dstField, srcField, converterFunc)`: Maps two fields that require a **custom conversion function**. On go1.27+ toolchains generic — `Convert[Dst, Src]` — so the field types are checked against the converter signature `func(context.Context, *model.ErrorCollector, Src) Dst`.
*   `c.Compute(dstField, expression)`: Maps a destination field that is **computed from an expression**. On go1.27+ toolchains generic — `Compute[T]` — so the expression's result type must match the field type.

All three accept **dotted field paths**, not just top-level fields: `c.Map(dst.Inner.ID, src.ID)` writes a leaf inside a nested destination struct, and `c.Map(dst.Flat, src.In.Value)` reads through a nested source struct. Pointer intermediates are handled — a `*T` on the source side guards the read (`if src.P != nil`), a `*T` on the destination side is nil-initialised before the write (`if dst.P == nil { dst.P = &T{} }`). Bad segments are reported at generation time. Explicit maps are emitted after the automatic field matches, so a leaf-path mapping overrides the copied leaf of a struct its ancestor was also mapped (`c.Map(dst.Inner.ID, src.ID)` beats `dst.Inner = convert(src.Inner)`'s copied ID).

## Conversion semantics and diagnostics

Fields are matched in this order: an explicit `c.Map`/`c.Convert` entry, then the normalized `json` tag, then the normalized field name. For each matched pair the generator emits, in order: the explicit converter, a matching `define.Rule`, or the default shape conversion — direct assignment for identical types, struct-to-struct via the discovered sub-converter, element-wise slices/arrays/maps, pointer un/re-wrapping, and finally a `DstT(src)` cast for castable leaf pairs (named scalar types, numeric pairs, `string` <-> `[]byte`/`[]rune`).

A leaf pair no rule and no cast covers (e.g. `int` -> `string`) still emits the honest raw assignment — which will not compile — but is also reported as a **generation warning** on the converter's doc comment and via `slog`, so the failure is visible before compile time. Pass `-strict` to make such warnings fail the run instead (see [Debugging](#debugging)).

Note on identical names across packages: two struct types that merely share a name (e.g. `a.User` and `b.User`) are *not* the same type — the generator converts them field by field. Struct identity is checked by canonical package-qualified name plus structural shape; same-name cross-package types whose fields differ still get a per-field converter, which is the intended behavior.

## Debugging

### Flags

| flag | use |
|---|---|
| `-file <define.go>` | the definition file (required) |
| `-output <file>` | where to write; its directory is the package the converters join (default `generated.go`) |
| `-dry-run` | print the generated code to stdout instead of writing it |
| `-log-level debug` | log the parsed definitions (`parsed_info`), discovered sub-conversions, and — when generated code does not parse — the full unformatted source |
| `-tags <expr>` | a build constraint written as the output's `//go:build` line (validated; e.g. `-tags e2e`) |
| `-strict` | fail instead of writing output when a field pair would not compile (generation warnings become errors) |
| `-check` | type-check the output inside its package with `go build` (via `-overlay`, so nothing is written first) and trace each error in the generated file back to its converter and field; needs the input packages to compile |

### Reading a failure

Every failure exits non-zero, writes nothing, and its first line says **who has to fix it**:

| first line says | cause | what to do |
|---|---|---|
| `invalid -tags "...": ... Fix the command-line arguments` | malformed `-tags` expression | fix the flag |
| `define file ... does not exist. Fix the command-line arguments` | `-file` names a missing path or a directory | pass the path of the define file |
| `define file ... does not parse (N errors). Fix the define file` | syntax error in the DSL file; every distinct error follows with a numbered excerpt | fix the define file |
| `define file ... is invalid at L:C: c.Map: ... Fix the define file at that position` | the definitions name something that does not resolve (unknown field in `c.Map`, bad `define.Rule` signature, ...); the position is the offending call, with an excerpt, plus `reached via` frames when `define.Convert` was called from a helper | fix the define file at that position, or the types |
| `-strict: N field pair(s) would not compile` | a field pair no rule/cast covers, or a `c.Compute` expression whose type the field cannot hold, listed as `converter: dst.Field: reason` | add a `define.Rule` for the type pair, or `c.Convert` the field; for `c.Compute`, fix the expression |
| `-check: generated code does not compile (N errors)` | a type error in the generated file the generator could not foresee (an arbitrary `c.Compute` expression, a type that does not implement a dst interface, ...); each error names the converter and field with an excerpt | fix the define file at that field; if it is right, report a generator bug |
| `-check: inconclusive: the package does not build for reasons outside the generated file` | the input package or a dependency does not compile, so the output cannot be judged | fix the input packages, or rerun without `-check` (regeneration itself never needs a compiling package) |
| `generated code does not parse ... This is a convert-define generator bug` | the generator emitted broken syntax; each error names the converter and field (`emitted by: converter convertAToB, field Items`) | report it with the message; rerun with `-log-level debug` for the raw source |
| `generated code uses N package(s) the generator did not import ... generator bug` | the generator used a package without registering its import; names the path and its first use | report it with the message |

`c.Compute` expressions are type-checked only where the type is knowable without a type checker: a `src` field path (`src.N`) or a call of a non-generic package func with one result (`funcs.Itoa(src.N)`). Other expressions (`src.S + "!"`, generic calls) are not checked at generation; `-check` catches them.

Without `-strict`, a field pair that will not compile still produces output: the warning is printed via `slog` and listed under "Generation warnings" in the converter's doc comment, and `go build` then fails on that assignment. Search the generated file for the `ec.Enter("Field")` line above the failing line to find the field.

### A typical loop

1. `go run ... -file define.go -dry-run` — check what would be generated without touching the package.
2. If a field looks wrong, rerun with `-log-level debug` and check `parsed_info` (which pairs and mappings the DSL produced) and the discovered sub-conversions.
3. Run with `-strict -check` in CI so an uncovered field pair, or any type error in the output, fails generation with the converter and field named rather than a later build.

`-check` is opt-in because regeneration must keep working while the package does not compile (e.g. a stale `generated.go` referencing deleted fields). With `-tags`, it builds with a tag set that satisfies the expression; GOOS/GOARCH terms in `-tags` are not applied, so such a constraint can exclude the file from the check.

## Role of `minigo` and `inspect`

The tool leans entirely on the interpreter it already runs in:
*   The `define` DSL file executes on the minigo stack-VM; `define.Convert`/`define.Rule` calls arrive quoted (AST, never evaluated) at special-form handlers in `internal/`.
*   `ctx.File()`/`ctx.Package()` wrap param exprs as `inspect.TypeExpr`, whose `SymbolID()` resolves `pkg.Type` to `{import path, name}` through the file's import table **without loading the package**.
*   `engine.Package` locates/parses/indexes exactly the packages the DSL touches; `inspect.FieldsOf`, `SignatureOf`, `DefOf` provide decl views; `TypeExpr`'s `Kind`/`Children`/`Unref`/`Resolve` walk type structure lazily.
*   Bound stdlib packages (e.g. `time`) have no source index, so known members surface as host pseudo-decls — the role `scanner.ExternalTypeOverride` used to play.
*   `generator.ImportManager` manages imports dynamically in the generated code (moved out of the vendored tree; it never depended on a scanner).

## Works on non-compiling input

Unlike `go/packages`-based tools, the generator does not require the input to compile — minigo reads ASTs and never type-checks, so **parseable code is enough**:

*   Type errors anywhere in `source`/`destination` (undefined identifiers in function bodies, fields of unresolvable types) do not stop generation.
*   The DSL file itself is never compiled — it is interpreted, and the mapping function literal is only walked as AST, not evaluated — so it may contain unused imports or dead code inside the literal that `go build` would reject (statements elsewhere in `main()` do execute).
*   Regeneration works while the generated package is broken: the typical field add/remove workflow leaves a stale `generated.go` referencing deleted fields, yet `convert -file define.go` runs fine and the fresh output un-breaks the package.
*   The only real boundary is **syntax**: a file that does not parse fails generation. Separately, names the DSL actually mentions (types in the `Convert` signature, fields in `c.Map` paths) must resolve — stale references there are generation errors, not ignored.
