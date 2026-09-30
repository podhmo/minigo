# Go Type Converter (`examples/convert-define`)

This directory contains `convert-define`, a command-line tool that automatically generates Go type conversion functions. It uses a vendored copy of `go-scan` (under `pkg/`) to parse a Go-based configuration file, understand the desired struct mappings, and then generate the necessary boilerplate code for converting one struct type to another.

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
	"github.com/podhmo/minigo/examples/convert-define/pkg/convert/convutil"
	"github.com/podhmo/minigo/examples/convert-define/pkg/convert/sampledata/destination"
	"github.com/podhmo/minigo/examples/convert-define/pkg/convert/sampledata/funcs"
	"github.com/podhmo/minigo/examples/convert-define/pkg/convert/sampledata/source"

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

## Role of `go-scan`

`go-scan` (vendored under `pkg/`, see `pkg/SOURCE.md`) is essential for this tool. It allows the parser to:
*   Read and understand the structure of Go types (structs, fields, etc.) **without compiling the code**.
*   Analyze the Go code in your `definitions.go` file as an Abstract Syntax Tree (AST).
*   Resolve type information across different packages, which is critical for handling complex models.
*   Manage imports dynamically in the generated code via its `ImportManager`.

<details>
<summary>Legacy Method: Annotation-Based Configuration</summary>

The original version of this tool used Go doc comments (annotations) and struct tags to define conversion rules. While this method is still supported by the `go run .../examples/convert` command, the `define` API is the recommended approach for new projects.

### Key Features (Legacy)

*   **Annotation-Driven**: Generation is triggered by a `@derivingconvert` annotation in the source struct's doc comment.
*   **Flexible Field Mapping**: Fields are matched automatically with a clear priority:
    1.  An explicit name in a `convert:"<name>"` tag.
    2.  A name in a `json:"<name>"` tag.
    3.  The normalized field name.
*   **Custom Conversion Logic**:
    *   Use the `convert:",using=<func>"` tag for field-specific custom conversion functions.
    *   Define global type-to-type conversion rules with `// convert:rule "<Src>" -> "<Dst>", using=<func>`.

### Annotation and Tag Reference (Legacy)

#### `@derivingconvert`
Triggers the generation of a conversion function. Placed in the doc comment of the source struct.
**Syntax**: `@derivingconvert(<DestinationType>[, option=value, ...])`

#### `// convert:rule`
Defines a global rule for type conversion or validation.
**Conversion Rule**: `// convert:rule "<SourceType>" -> "<DestinationType>", using=<FunctionName>`
**Validator Rule**: `// convert:rule "<DestinationType>", validator=<FunctionName>`

#### `convert` Struct Tag
Controls the conversion of a specific field.
**Syntax**: `` `convert:"[destinationFieldName],[option=value],..."` ``
*   `[destinationFieldName]`: Maps to a different field name in the destination struct. Use `-` to skip the field.
*   `using=<funcName>`: Use a custom function for this field's conversion.
*   `required`: Reports an error if a source pointer field is `nil`.

### How to Use (Legacy)

1.  **Annotate your code**: Add `@derivingconvert` annotations to your source structs and any necessary `convert` tags or `// convert:rule` comments.
2.  **Run the tool**:
    ```bash
    go run github.com/podhmo/go-scan/examples/convert \
      -pkg "github.com/your/project/models" \
      -output "github.com/your/project/models/generated_converters.go"
    # (the legacy annotation tool lives in the upstream go-scan repository; the
    # converter libraries it shares with this tool are vendored under pkg/convert)
    ```

</details>
