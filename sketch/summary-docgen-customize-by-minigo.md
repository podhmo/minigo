# Customizing `docgen` Analysis with `minigo` Patterns

The `docgen` tool uses a symbolic execution engine (`symgo`) to analyze Go code and generate OpenAPI specifications. While it has built-in support for standard `net/http` and `encoding/json` patterns, real-world applications often use custom helper functions for handling requests and responses. To support these, `docgen` allows you to define custom analysis patterns using a Go configuration file, which is interpreted by `minigo`.

This document explains how to create a custom patterns file and details all the available pattern types.

## Creating a Patterns Configuration File

To provide custom patterns to `docgen`, you create a Go file (e.g., `patterns.go`) in your project. This file must have a `//go:build minigo` build tag and define a global variable named `Patterns`.

You pass the path to this file to `docgen` using the `--patterns` flag:
```bash
docgen --patterns=./path/to/your/patterns.go <your-api-package>
```

### Structure of the Configuration File

The configuration file looks like this:

```go
//go:build minigo
// +build minigo

package main

import "github.com/podhmo/go-scan/examples/docgen/patterns"

// Patterns defines a list of custom patterns for the docgen tool.
var Patterns = []patterns.PatternConfig{
    // ... your pattern definitions here ...
}
```

The `Patterns` variable is a slice of `patterns.PatternConfig` structs. Each struct defines one custom analysis rule.

## `PatternConfig` Struct

The `patterns.PatternConfig` struct has the following fields:

- `Key` (string): **Required.** The fully qualified function or method name to match.
  - For functions: `"<module-path>/<package-name>.<function-name>"`
    - Example: `"myapp/helpers.RenderJSON"`
  - For methods: `"<module-path>/<package-name>.<receiver>.<method-name>"`
    - Example: `"myapp/models.(*User).GetProfile"`
- `Type` (PatternType): **Required.** The type of analysis to perform. See the "Pattern Types" section below for all available options.
- `ArgIndex` (int): **Required.** The 0-based index of the function argument to analyze. For parameter types, this refers to the argument holding the parameter's **value**.
- `NameArgIndex` (int): Required for parameter types. The 0-based index of the argument holding the parameter's **name**.
- `StatusCode` (string): Required for `CustomResponse`. The HTTP status code for the response.
- `Description` (string): Optional description for parameters.

## Pattern Types

Here are the available values for the `Type` field.

---

### `responseBody`

Treats a function argument as the success (200 OK) response body. `docgen` will generate a schema from the argument's type and place it in the `200` response.

- **`ArgIndex`**: The index of the argument containing the data to be encoded as the response body.

**Example:**
Your code has a helper `helpers.RenderJSON(w, r, myData)`.

```go
// patterns.go
{
    Key:      "myapp/helpers.RenderJSON",
    Type:     patterns.ResponseBody,
    ArgIndex: 2, // The `myData` argument
}
```

---

### `requestBody`

Treats a function argument as the request body. `docgen` will generate a schema from the argument's type and place it in the `requestBody` section of the operation. The argument at `ArgIndex` is expected to be a pointer to the struct that the request body should be decoded into.

- **`ArgIndex`**: The index of the argument that the request body is decoded into. Must be a pointer.

**Example:**
Your code has a helper `helpers.DecodeJSON(r, &input)`.

```go
// patterns.go
{
    Key:      "myapp/helpers.DecodeJSON",
    Type:     patterns.RequestBody,
    ArgIndex: 1, // The `&input` argument
}
```

---

### `defaultResponse`

Treats a function argument as the `default` response body. This is useful for defining a standard error response for an operation. It does not take a status code.

- **`ArgIndex`**: The index of the argument containing the error data structure.

**Example:**
Your code has a helper `helpers.RenderDefaultError(w, r, err)`.

```go
// patterns.go
{
    Key:      "myapp/helpers.RenderDefaultError",
    Type:     patterns.DefaultResponse,
    ArgIndex: 2, // The `err` argument
}
```

---

### `customResponse`

Treats a function argument as a response body for a specific, non-200 status code. This is useful for defining specific error responses (e.g., 400, 404, 500).

- **`ArgIndex`**: The index of the argument containing the response data structure.
- **`StatusCode`**: **Required.** The HTTP status code as a string (e.g., `"400"`).

**Example:**
Your code has a helper `helpers.RenderBadRequest(w, r, validationError)`.

```go
// patterns.go
{
    Key:        "myapp/helpers.RenderBadRequest",
    Type:       patterns.CustomResponse,
    StatusCode: "400",
    ArgIndex:   2, // The `validationError` argument
}
```

---

### `path`, `query`, `header`

Extracts a parameter from a function call where the parameter's name is passed as an argument. This is useful when you have generic helper functions for extracting data from a request.

- **`Type`**: `patterns.PathParameter`, `patterns.QueryParameter`, or `patterns.HeaderParameter`.
- **`NameArgIndex`**: **Required.** The index of the argument containing the parameter's **name** (must be a string literal).
- **`ArgIndex`**: **Required.** The index of the argument containing the parameter's **value**. The schema will be inferred from this argument's type. For helpers that *return* the value, this can often be pointed to an argument like `*http.Request` and the schema will default to a string.
- **`Description`**: An optional description for the parameter.

**Example:**
Your code has a helper `framework.GetQuery(r, "sort") string`.

```go
// patterns.go
{
    Key:          "myapp/framework.GetQuery",
    Type:         patterns.QueryParameter,
    NameArgIndex: 1, // The "sort" argument
    ArgIndex:     0, // The `*http.Request` argument, schema will default to string
}
```
This single pattern will now work for all calls to `GetQuery`, extracting the parameter name from the second argument of each call.

---

## Testing Custom Patterns

When developing custom patterns, it's essential to have a reliable testing strategy. The `go-scan` repository provides the `scantest` package, a testing library designed to create isolated, in-memory tests for tools built on `go-scan`, including `docgen`. This approach avoids the complexities of file paths and module resolution that can arise when running tests that span multiple `go.mod` files.

### Example: Verifying Key Generation

A common requirement is to verify that `docgen` correctly generates the internal matching key from a type-safe `Fn` reference in your `patterns.go` file. The following example demonstrates how to write such a test using `scantest`.

**1. Test File Setup**

First, create a test file (e.g., `docgen_test.go`) and define the source code for your test module as string constants. This includes a `go.mod` file, the Go source for the types you want to reference, and the `patterns.go` script itself.

```go
// my_test.go
package main // assuming this is in the docgen package

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	goscan "github.com/podhmo/go-scan"
	"github.com/podhmo/go-scan/scantest"
)

const testGoMod = `
module my-test-module

go 1.21

// The replace directive is crucial for allowing the test module
// to find the main go-scan module's packages. The path must be
// relative to the *project root*, not the test file.
replace github.com/podhmo/go-scan => ".."
`

const testFooGo = `
package foo

type Foo struct{}
func (f *Foo) Bar() {} // Pointer receiver
func (f Foo) Qux() {}   // Value receiver
func Baz() {}           // Standalone function
`

const testPatternsGo = `
//go:build minigo
package main
import "my-test-module/foo"

// A stub for the real PatternConfig
type PatternConfig struct {
	Fn   any
	Type string
}

var (
	v = foo.Foo{}
	p = &foo.Foo{}
)

var Patterns = []PatternConfig{
	{Fn: (*foo.Foo)(nil).Bar},
	{Fn: foo.Baz},
	{Fn: v.Qux},
	{Fn: p.Bar},
}
`
```
*Note: The path in the `replace` directive (`".."`) should be adjusted based on the location of your test file relative to the project root.*

**2. The Test Function**

Next, write the test function using `scantest.WriteFiles` and `scantest.Run`.

```go
// my_test.go (continued)

func TestKeyGeneration(t *testing.T) {
	files := map[string]string{
		"go.mod":      testGoMod,
		"foo/foo.go":  testFooGo,
		"patterns.go": testPatternsGo,
	}

	// scantest.WriteFiles creates a temporary directory with the file layout.
	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	// The action function contains the core test logic.
	action := func(ctx context.Context, s *goscan.Scanner, pkgs []*goscan.Package) error {
		logger := newTestLogger(io.Discard) // assuming newTestLogger is available

		// Use the loader from docgen to process the patterns file.
		// Note: The path must be absolute, constructed from the temp dir.
		patternsPath := filepath.Join(dir, "patterns.go")
		loadedPatterns, err := LoadPatternsFromConfig(patternsPath, logger, s)
		if err != nil {
			t.Fatalf("LoadPatternsFromConfig failed: %+v", err)
		}

		// Verify the generated keys.
		expectedKeys := map[string]bool{
			"my-test-module/foo.(*Foo).Bar": true,
			"my-test-module/foo.Foo.Qux":   true,
			"my-test-module/foo.Baz":       true,
		}

		foundKeys := make(map[string]bool)
		for _, p := range loadedPatterns {
			foundKeys[p.Key] = true
		}

		if diff := cmp.Diff(expectedKeys, foundKeys); diff != "" {
			t.Errorf("key mismatch (-want +got):\n%s", diff)
		}
		return nil
	}

	// scantest.Run handles the scanner setup and execution.
	// Scanning "." tells it to process the entire temporary directory.
	if _, err := scantest.Run(t, dir, []string{"."}, action); err != nil {
		t.Fatalf("scantest.Run failed: %v", err)
	}
}
```

This approach provides a hermetic, reliable way to test `docgen`'s `minigo`-based features without interfering with the main project's build system or file structure.
