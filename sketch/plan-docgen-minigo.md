# Plan: Extensible `docgen` with `minigo`

This document outlines the plan to enhance the `docgen` tool by allowing users to define custom analysis patterns using external configuration files, interpreted by the `minigo` engine. This will make `docgen` more adaptable to different coding styles and custom helper functions without requiring users to modify and recompile the tool.

## 1. Motivation

Currently, `docgen` relies on a hardcoded set of patterns (e.g., for `encoding/json.Decode`) to identify request and response objects in HTTP handlers. This is inflexible. If a project uses custom wrappers or helper functions for JSON decoding/encoding, `docgen` cannot analyze them, and the generated OpenAPI specification will be incomplete.

The goal is to allow users to provide a configuration file that teaches `docgen` about these custom patterns.

## 2. Configuration File Design

Instead of a simple data format like JSON or YAML, or a custom `.minigo` script, we will use standard Go files as the configuration medium. This provides several advantages:

-   **Type Safety**: The configuration will be written in Go, allowing `gopls` and other standard Go tools to provide static analysis, autocompletion, and type checking.
-   **Expressiveness**: Users can use Go's syntax, including variables and helper functions, to construct their patterns, reducing boilerplate.
-   **Simplicity**: Users do not need to learn a new scripting language; they can use the language they are already familiar with.

The configuration file should have a build tag `//go:build minigo` at the top. This prevents the file from being included in a normal build of the user's project while clearly indicating its purpose for `go-scan` tools. The `minigo` interpreter will execute this file to extract the configuration.

### Example Configuration (`docgen.go`)

```go
//go:build minigo

// This is a configuration file for docgen.
package main

import "github.com/podhmo/go-scan/examples/docgen/patterns"

// The 'Patterns' variable will be read by docgen.
var Patterns = []patterns.PatternConfig{
    // Teach docgen about a custom JSON decoding function.
    {
        Key:      "github.com/my-org/my-app/utils.DecodeJSON",
        Type:     "requestBody",
        ArgIndex: 1, // The 2nd argument is the one to be decoded into.
    },

    // Teach docgen about a custom JSON response function.
    {
        Key:      "github.com/my-org/my-app/utils.SendJSON",
        Type:     "responseBody",
        ArgIndex: 2, // The 3rd argument is the data being sent.
    },
}
```

## 3. Implementation Details

### Step 1: Define `PatternConfig` Struct

A new struct, `PatternConfig`, will be defined in `examples/docgen/patterns/patterns.go`. This struct will serve as the data structure for user-defined patterns.

```go
// PatternConfig defines a user-configurable pattern for docgen analysis.
// It maps a function call to a specific analysis type.
type PatternConfig struct {
    // Key is the fully-qualified function or method name to match.
    // e.g., "github.com/my-org/my-app/utils.DecodeJSON"
    // e.g., "(*net/http.Request).Context"
    Key string

    // Type specifies the kind of analysis to perform.
    // Supported values: "requestBody", "responseBody".
    Type string

    // ArgIndex is the 0-based index of the function argument to analyze.
    // For "requestBody", this is the argument that will be decoded into.
    // For "responseBody", this is the argument that will be encoded from.
    ArgIndex int
}
```

### Step 2: Implement the Pattern Loader

A new component, `loader.go`, will be created in the `docgen` example. It will be responsible for loading patterns from a Go configuration file.

-   **Function**: `LoadPatterns(filePath string) ([]patterns.Pattern, error)`
-   **Process**:
    1.  Initialize a `minigo` interpreter.
    2.  Make the `github.com/podhmo/go-scan/examples/docgen/patterns` package available to the script. This will be done by providing `minigo` with the source code of `patterns.go` so it can interpret the `import` statement.
    3.  Read the user's config file (e.g., `patterns.go`).
    4.  Execute the file content using `minigo.Eval()`.
    5.  Extract the `Patterns` variable from the interpreter's environment.
    6.  Use `minigo.Result.As()` to convert the `minigo` object into a Go `[]patterns.PatternConfig` slice.
    7.  Translate the `[]PatternConfig` slice into the internal `[]patterns.Pattern` slice that the `symgo` engine uses. This will involve a mapping from the `Type` string to the appropriate `symgo.IntrinsicFunc` (e.g., `handleDecode`, `handleEncode`).

### Step 3: Integrate into `docgen`

The `docgen` main application will be updated to use the new loader.

1.  A new command-line flag, `--patterns <path/to/patterns.go>`, will be added to `examples/docgen/main.go`.
2.  If the flag is provided, `main()` will call `LoadPatterns()` to get the custom patterns.
3.  The custom patterns will be passed to the `Analyzer`.
4.  The `Analyzer`'s `buildHandlerIntrinsics` method will be modified to merge the default patterns with the loaded custom patterns, making them active during symbolic execution.

### Step 4: Testing

A comprehensive integration test will be added to `examples/docgen/main_test.go`:

1.  A new test data directory will be created containing:
    -   A sample API using custom helper functions.
    -   A `patterns.go` config file with `//go:build ignore`.
2.  A new test case will run `docgen` against this sample API, using the `--patterns` flag.
3.  The test will compare the generated OpenAPI output against a golden file to ensure the custom patterns were correctly applied and the request/response bodies were properly identified.

## 4. Future Directions

The current implementation uses `minigo` to evaluate a configuration file that returns a data structure (a slice of maps). The analysis logic itself (the `Apply` function in the `Pattern` struct) still resides in compiled Go code within `docgen`.

A powerful future enhancement would be to allow the analysis logic itself to be defined in the `minigo` script. This would look something like this:

```go
// future-docgen.go
//go:build minigo

package main

// Define a pattern for a custom response function.
var Patterns = []map[string]any{
    {
        "Key": "github.com/my-org/my-app/utils.SendCustom",

        // Instead of a "Type", provide a minigo function.
        "Apply": func(interp, analyzer, args) {
            // This code would be executed by minigo within symgo.
            op := analyzer.OperationStack()[0]

            // Script would need access to symgo's object model
            // and openapi struct definitions to manipulate the spec.
            schema := interp.BuildSchemaForType(args[2])
            op.Responses["200"].Content["application/json"].Schema = schema
        },
    },
}
```

### Challenges

To achieve this, several enhancements to the `symgo` and `minigo` bridge would be required:
-   **Function Passing**: A mechanism to pass a `minigo` function (`*object.Function`) into `symgo` and have `symgo` call it as an intrinsic.
-   **FFI for `symgo`**: The `minigo` script would need access to the `symgo.Analyzer` instance and the `openapi` data structures. This would require a deeper foreign function interface (FFI) to expose parts of the `symgo` and `docgen` internals to the script environment.

This would represent a significant step towards a fully scriptable analysis engine, offering the ultimate level of flexibility.

## 5. Conclusion

This approach provides a powerful and flexible way for users to extend `docgen`'s analysis capabilities. By using Go files for configuration, it maintains a high degree of usability and leverages the existing Go ecosystem for tooling support, aligning well with the overall philosophy of the `go-scan` project.
