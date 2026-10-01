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

The public API is housed in the `github.com/podhmo/minigo/examples/convert-define/define` package.

*   `define.Convert(mapFunc)`: Defines a conversion between two struct types. The source and destination types are inferred from the signature of the mapping function, which must be `func(c *Config, dst *DstType, src *SrcType)`.
*   `define.Rule(customFunc)`: Defines a global, reusable conversion rule for a specific type-to-type conversion (e.g., `time.Time` to `string`).
*   `c.Map(dstField, srcField)`: Maps a source field to a destination field with a **different name**.
*   `c.Convert(dstField, srcField, converterFunc)`: Maps two fields that require a **custom conversion function**.
*   `c.Compute(dstField, expression)`: Maps a destination field that is **computed from an expression**.

## Role of `minigo` and `inspect`

The tool leans entirely on the interpreter it already runs in:
*   The `define` DSL file executes on the minigo stack-VM; `define.Convert`/`define.Rule` calls arrive quoted (AST, never evaluated) at special-form handlers in `internal/`.
*   `ctx.File()`/`ctx.Package()` wrap param exprs as `inspect.TypeExpr`, whose `SymbolID()` resolves `pkg.Type` to `{import path, name}` through the file's import table **without loading the package**.
*   `engine.Package` locates/parses/indexes exactly the packages the DSL touches; `inspect.FieldsOf`, `SignatureOf`, `DefOf` provide decl views; `TypeExpr`'s `Kind`/`Children`/`Unref`/`Resolve` walk type structure lazily.
*   Bound stdlib packages (e.g. `time`) have no source index, so known members surface as host pseudo-decls — the role `scanner.ExternalTypeOverride` used to play.
*   `generator.ImportManager` manages imports dynamically in the generated code (moved out of the vendored tree; it never depended on a scanner).
