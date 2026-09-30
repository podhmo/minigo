> [!NOTE]
> This feature has been implemented.

# Final API Specification for IDE-Native `convert` Configuration

## 1. Introduction

This document provides the final API specification for a new, IDE-native method of configuring the `convert` tool. This approach ensures that the entire configuration is written as **statically valid Go code that passes the type checker**, providing a seamless IDE experience.

The core principle is to define **exceptions and custom logic** for the conversion process. By default, the generator will automatically map all fields with matching names. The API described here is used to override this default behavior.

The configuration is defined by calling methods on a **configurator object** within a function literal. This allows a `minigo`-based parser to analyze the Abstract Syntax Tree (AST) of these method calls to deduce the user's intent.

## 2. The `define` API Specification

The public API is housed in a new `define` package. Its purpose is to define conversion rules.

### 2.1. Default Behavior: Implicit Mapping

By default, the generator will automatically map any field in the source struct to a field in the destination struct if their names are identical (or become identical after normalization, e.g., `last_name` matches `LastName`). Users **do not** need to specify these mappings. The `define` API is for handling cases that fall outside this default behavior.

### 2.2. Top-Level Functions

#### `define.Convert()`
Defines a conversion between two struct types. The source and destination types are inferred from the signature of the mapping function.

**Signature:**
```go
func Convert(mapFunc any)
```
-   `mapFunc any`: A function literal with the signature `func(c *Config, dst *DestType, src *SrcType)`.

#### `define.Rule()`
Defines a global, reusable conversion rule for a specific type-to-type conversion.

**Signature:**
```go
func Rule(customFunc any)
```
-   `customFunc any`: A function identifier (e.g., `convutil.TimeToString`). The parser infers the source type, destination type, and import path from the function's signature.

### 2.3. The Configurator Pattern

The mapping function passed to `define.Convert` receives a `*define.Config` object as its first argument. This object, conventionally named `c`, provides methods to define field-level exceptions to the default mapping behavior.

**`c.Map(dstField, srcField any)`**
Defines a mapping between two fields with **different names**.

-   **Parser Interpretation:** The parser creates a mapping rule between the two fields. This is only necessary when the source and destination field names do not match.

**`c.Convert(dstField, srcField, converterFunc any)`**
Defines a mapping that requires a custom conversion function.

-   **Parser Interpretation:** The parser creates a rule to use the `converterFunc` for the specified field conversion. This is used when the default assignment or a global `Rule` is not sufficient.

**`c.Compute(dstField, expression any)`**
Defines a mapping for a destination field that is computed from an expression.

-   **Parser Interpretation:** The parser captures the `expression`'s AST and creates a `computed=` rule.

## 3. Final Example: Definitive Mapping File

This example demonstrates the final, refined API. Note how it only specifies the exceptions. `dst.Details`, `dst.CreatedAt`, etc., are not mentioned because they will be mapped automatically.

```go
//go:build codegen
// +build codegen

package main

import (
	"github.com/podhmo/go-scan/examples/convert/convutil"
	"github.com/podhmo/go-scan/examples/convert/sampledata/destination"
	"github.com/podhmo/go-scan/examples/convert/sampledata/funcs"
	"github.com/podhmo/go-scan/examples/convert/sampledata/source"

	"github.com/podhmo/go-scan/tools/define" // Using the new 'define' package
)

func main() {
	// Define global rules for types that cannot be mapped automatically.
	define.Rule(convutil.TimeToString)
	define.Rule(convutil.PtrTimeToString)

	// Define the conversion from SrcUser to DstUser, only specifying the exceptions.
	define.Convert(func(c *define.Config, dst *destination.DstUser, src *source.SrcUser) {
		// Exception 1: Different names
		c.Map(dst.UserID, src.ID)
		// Exception 2: Different names and a custom function.
		c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)
		// Exception 3: A computed field.
		c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))
	})

	// Define conversion for a nested struct with name differences.
	define.Convert(func(c *define.Config, dst *destination.DstAddress, src *source.SrcAddress) {
		c.Map(dst.FullStreet, src.Street)
		c.Map(dst.CityName, src.City)
	})

	// Define conversion for another struct with name differences and a custom function.
	define.Convert(func(c *define.Config, dst *destination.DstInternalDetail, src *source.SrcInternalDetail) {
		c.Map(dst.ItemCode, src.Code)
		c.Convert(dst.LocalizedDesc, src.Description, funcs.Translate)
	})
}
```

## 4. Parser and Generator Interaction

-   **Parser's Role**: The parser translates the `define` API calls into the `model.ParsedInfo` IR. It understands that it is only receiving exceptions to the default behavior.
-   **Generator's Role**: The generator's logic is enhanced. Before generating the conversion for a struct pair, it first performs a pass to identify and automatically map all fields with matching names that have not been explicitly configured by the parser. It then processes the explicit rules from the IR, which take precedence. This two-phase approach (default mapping + explicit overrides) ensures correctness and implements the desired user experience.

## 5. Required `minigo` Enhancements for AST Analysis

The `define` script paradigm, where the tool analyzes the AST of function arguments, requires specific features in the underlying `minigo` interpreter. The standard Go runtime cannot inspect the AST of a function it receives as an argument; the AST is lost after compilation. `minigo`, as an interpreter, can be enhanced to provide this capability.

The required enhancements fall into three categories:

### 5.1. Special Forms (Unevaluated Arguments)
`minigo` needs a mechanism to treat certain functions as "special forms" (or macros). When the interpreter's `eval` loop encounters a call to a function registered as a special form, it must **not** evaluate its arguments beforehand. Instead, it should pass the raw, unevaluated argument expressions to the function's implementation.

-   **Implementation**: This could be a new option when registering a Go function with the interpreter, e.g., `interp.RegisterSpecial("define.Mapping", goMappingFunc)`.

### 5.2. AST-as-an-Object
To pass an unevaluated AST expression through the interpreter, it must be wrapped in a `minigo/object` type.

-   **Implementation**: A new object type, `object.AstNode`, would be created. It would have a field like `Node go/ast.Node`. When a special form is called, the interpreter would wrap the AST node of the unevaluated argument (e.g., the `ast.FuncLit` for the mapping function) into this `object.AstNode` and pass it along.

### 5.3. Go Interop for AST
The Go function that implements the special form must be able to receive the raw `go/ast.Node`.

-   **Implementation**: The `minigo` Go interoperability layer must be updated to recognize `object.AstNode`. When calling a Go function that expects a `go/ast.Node` argument, the interop layer would unwrap the `object.AstNode` and pass the underlying `Node` to the Go function.

With these enhancements, the `define` tool's workflow would be:
1.  Initialize a `minigo` interpreter.
2.  Register the Go implementations of `define.Mapping`, `c.Assign`, etc., as special forms whose arguments should not be evaluated.
3.  Execute the user's `define.go` script.
4.  When `minigo` encounters `define.Mapping(func() { ... })`, it sees `define.Mapping` is a special form, wraps the `ast.FuncLit` of the function in an `object.AstNode`, and passes it to the Go implementation.
5.  The Go implementation receives the `ast.FuncLit` and can now walk it to parse the mapping rules.

## 6. Implementation Task List

This section provides an ordered, incremental task list for implementing this feature.

### Phase 1: `minigo` Core Enhancements
*These tasks modify the core `minigo` interpreter to support AST analysis.*

1.  **Implement `object.AstNode`**: Create a new type in the `minigo/object` package to wrap a `go/ast.Node`.
2.  **Implement Special Form Mechanism**: Modify the `minigo` evaluator to recognize "special form" functions and to not evaluate their arguments.
3.  **Enhance Go Interop Layer**: Update the interoperability layer to correctly unwrap `object.AstNode` and pass a raw `ast.Node` to a Go function that expects it.
4.  **Add Unit Tests**: Write unit tests within the `minigo` package to verify that a Go function registered as a special form can correctly receive the AST of its arguments.

### Phase 2: `define` Tool and Parser Implementation
*These tasks build the new `define`-based converter tool.*

1.  **Create `examples/convert/define` Package**: Create the new package containing the stub API functions (`Convert`, `Rule`, `Mapping`) and the empty `Config` struct.
2.  **Create CLI Entrypoint**: Create a new command (`examples/convert/cmd/convert-define`) for the new tool.
3.  **Implement Core Parser**: In the new command, implement the main parser logic that initializes the enhanced `minigo` interpreter and registers the `define` API functions as special forms.
4.  **Implement `define.Rule` Parsing**: Implement the logic to handle `define.Rule(customFunc)` calls. This involves using `go-scan` to resolve the function, inferring types from its signature, and creating a `model.TypeRule`.
5.  **Implement `define.Mapping` Parsing**: Implement the logic to handle the `ast.FuncLit` passed to `define.Mapping`. This involves setting up a sub-walker for the function body.
6.  **Implement `Config` Method Parsing**: Implement the logic within the sub-walker to parse calls to `c.Assign`, `c.Convert`, and `c.Compute`. This includes analyzing their arguments (selectors and expressions) and creating the appropriate `model.FieldMap` or `model.ComputedField` rules.
7.  **IR Construction**: Ensure the parser correctly assembles all the parsed rules into a single, valid `model.ParsedInfo` struct.

### Phase 3: Generator Integration and Finalization
*These tasks integrate the new parser with the existing generator and finalize the feature.*

1.  **Enhance Generator for Implicit Mapping**: Modify the existing `generator` to automatically map all fields with matching names *before* it processes the explicit rules from the `ParsedInfo` struct.
2.  **Integrate Parser and Generator**: In the `cmd/convert-define` main function, plumb the `ParsedInfo` struct from the new parser into the enhanced generator.
3.  **Add Integration Tests**: Create a comprehensive test suite for the `convert-define` command. This should include a `define.go` script as input and assert that the generated Go code is correct.
4.  **Write User Documentation**: Update the project's `README.md` and any other relevant user-facing documentation to explain the new, preferred method for defining conversions.
