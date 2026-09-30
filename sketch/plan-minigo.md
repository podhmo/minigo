> [!NOTE]
> This feature has been implemented.

# Plan for minigo Implementation

## 1. Introduction and Concept

**minigo** will be a reimplementation of the `minigo` concept, evolving from a simple example into a robust, embeddable script engine for Go applications. The primary use case for `minigo` is to serve as a **configuration language**. It will allow developers to write configuration files using a subset of Go syntax, which can then be interpreted at runtime by a Go application.

The core goals of `minigo` are:
- **Readability and Familiarity**: Leverage Go-like syntax for configuration and scripting, making it intuitive for Go developers.
- **Go Interoperability**: The core feature is the ability to **unmarshal** script results directly into Go structs in a type-safe manner. This allows `minigo` to act as a powerful, dynamic, and type-safe replacement for static configuration files like JSON or YAML.
- **Rich Type System**: Utilize `go-scan` to understand Go types defined within the host application. This enables the interpreter to perform more intelligent operations and provide better error messages.
- **Developer Experience**: Provide clear, human-readable stack traces on errors to simplify debugging of scripts.
- **Simplicity**: Prioritize clarity of implementation and ease of use over raw execution speed. The engine does not need to be a JIT or a highly optimized AOT compiler.

`minigo` will **not** be a full Go implementation. It will interpret a curated subset of the language focused on its role as a configuration and scripting engine. It will strictly avoid `go/packages` and `go/types` in favor of the lazy, AST-based approach provided by `go-scan`.

### Unsupported Features

To maintain simplicity and focus on its core use case, `minigo` will intentionally not support certain advanced Go features. While the Go parser will recognize these syntactic constructs, the `minigo` interpreter will produce a runtime error when it encounters them. This ensures that scripts remain within the bounds of the engine's capabilities.

The following features are explicitly **unsupported**:

-   **Concurrency**: `go` statements, `chan` types, and `select` statements. `minigo` is a single-threaded interpreter.
-   **Panic, and Recover**: The `panic`/`recover` mechanism are not supported. Errors should be handled through explicit return values.
-   **Interfaces**: Full support for `interface` types, type assertions, and type switches is not a goal. Go interoperability will handle specific interface needs on the host side.
-   **Unsafe Operations**: The `unsafe` package and its functionalities are not supported.

This clear boundary allows `minigo` to be a predictable and secure scripting environment for configuration tasks.

## 2. Core Architecture

The `minigo` engine will be composed of several key components:

1.  **Entry Point (`minigo.Run`)**: A simple, library-style API for executing scripts. It will accept the script source (as a string or file path) and an optional context for passing in Go variables and functions.

2.  **Parser (Powered by `go-scan`)**: `minigo` will not have its own parser. Instead, it will use `go/parser` to get the AST of the script, and a dedicated `goscan.Scanner` instance to resolve types and parse imported Go packages. This allows `minigo` to directly reuse Go's AST structures (`go/ast`).

3.  **Interpreter Engine (`eval` loop)**: The heart of `minigo`. This component will be an AST-walking interpreter. It will traverse the AST provided by the parser and evaluate each node recursively.
    - It will manage the execution state, including scopes and environments.
    - It will handle control flow statements like `if`, `for`, and function calls.

4.  **Object System (`object.Object`)**: A set of internal types used to represent data within the interpreter at runtime. This will be similar to `minigo` but expanded for better Go interoperability.
    - `object.Integer`, `object.String`, `object.Boolean`
    - `object.StructInstance`, `object.Function`
    - A special `object.GoValue` that wraps a `reflect.Value`, allowing native Go values to be passed into the script.

5.  **Environment Model (`object.Environment`)**: A structure for managing variable and symbol lookups. It will support nested scopes to correctly handle global, function, and block-level variables.

6.  **Go Interop Layer (The `reflect` Bridge)**: This is a critical component for the configuration use case.
    - **Go -> minigo**: It will allow the host application to "inject" Go variables and functions into the `minigo` environment. These will be wrapped in `object.GoValue` or a `BuiltinFunction` equivalent.
    - **minigo -> Go**: It will provide a mechanism to extract the result of a script's evaluation and convert it from a `minigo` internal object back into a Go variable using `reflect.Value.Set`.

7.  **Error Handling & Call Stack**: A mechanism to track function calls within the script. If an error occurs, this call stack will be used to generate a formatted, easy-to-read error message, including file names and line numbers.

## 3. Detailed Component Design

### 3.1. Entry Point API

The primary API will be a function within the `minigo` package.

```go
package minigo

// Options configures the interpreter environment.
type Options struct {
    // Globals allows injecting Go variables into the script's global scope.
    // The map key is the variable name in the script.
    // The value can be any Go variable, which will be made available via reflection.
    Globals map[string]any

    // Source is the script content.
    Source []byte

    // Filename is the name of the script file, used for error messages.
    Filename string

    // EntryPoint is the name of the function to execute.
    EntryPoint string
}

// Result holds the outcome of a script execution.
type Result struct {
    // Value is the raw minigo object returned by the script.
    Value object.Object

    // Error contains any runtime error, pre-formatted with a stack trace.
    Error error
}

// Run executes a minigo script.
func Run(ctx context.Context, opts Options) (*Result, error) {
    // ... implementation ...
}
```

### 3.2. Parsing with `go-scan`

- A `minigo.Interpreter` struct will hold a `goscan.Scanner` instance.
- When `Run` is called, it will first use `go/parser.ParseFile` to get the AST for the user's script (`.mgo` file).
- The interpreter will walk the declarations of the script AST. When it encounters an `import` statement, it will use its internal `goscan.Scanner` to lazily `ScanPackageFromImportPath` when a symbol from that package is first accessed (e.g., `fmt.Println`).
- `go-scan` will handle finding the package, parsing it, and returning its exported symbols (`PackageInfo`), which the interpreter will then load into its environment.

### 3.2.1. Lazy Loading of Imports and Symbols

To improve performance and minimize unnecessary file I/O, `minigo` will adopt a lazy-loading strategy for package imports. Instead of scanning an entire package upon encountering an `import` statement, the work will be deferred until a symbol from that package is actually accessed. This approach is particularly beneficial for a configuration engine, where scripts may import large packages but only use a few constants or functions.

The mechanism will work as follows:

1.  **Initial `import` Evaluation**: When the interpreter evaluates an `import "path/to/pkg"` statement, it will **not** immediately scan the package. Instead, it will:
    -   Create a placeholder `object.Package` in the environment.
    -   This proxy object will store the import path (`"path/to/pkg"`) and the package name (derived heuristically from the path, e.g., `pkg`).
    -   Crucially, the `Info` field (which holds the `*scanner.PackageInfo`) of this `object.Package` will be `nil`, signaling that its symbols have not yet been loaded.

2.  **Symbol Access Trigger**: The actual loading is triggered when the script attempts to access a member of the package, for example, in an expression like `pkg.Symbol`.

3.  **On-Demand Symbol Resolution**: When the evaluator encounters this selector expression, it will:
    -   Check if the `pkg` object's `Info` field is `nil`.
    -   If it is `nil`, it initiates the symbol resolution process. It will invoke a new function in `go-scan`, such as `FindSymbolInPackage(importPath, symbolName)`.

4.  **File-by-File Scanning in `go-scan`**: The `FindSymbolInPackage` function will avoid scanning the entire package at once.
    -   It will get a list of all `.go` files in the package directory.
    -   It will scan files one by one (or in small batches), checking for the requested `symbolName`.
    -   The `go-scan` cache will ensure that files are not parsed more than once across the lifetime of the scanner.
    -   Once the symbol is found, the scan stops. This prevents the engine from reading and parsing files that are irrelevant to the current operation.

5.  **Populating the Package Object**:
    -   `go-scan` returns the `*scanner.PackageInfo` derived from the file(s) that were just scanned.
    -   The interpreter updates the original `object.Package` proxy: its `Info` field is populated with the new information, and the resolved symbol is cached in its `Members` map.
    -   The evaluator then proceeds with the original expression, now with the loaded symbol.

This lazy strategy ensures that `minigo` performs only the minimum work required to execute a script, leading to faster startup times and more efficient resource usage, while the underlying `go-scan` caching mechanism keeps subsequent lookups within the same package fast.

### 3.3. Object System and Go Interop

The `object.Object` interface will be the foundation. The key to interoperability is how we bridge `minigo` objects and Go's `reflect.Value`.

- **From Go to minigo**:
  - When `Options.Globals` is provided, the interpreter will iterate through the map.
  - For each entry, it will use `reflect.ValueOf(value)` to get a `reflect.Value`.
  - This `reflect.Value` will be wrapped in a new `object.GoValue{Value: reflect.Value}`.
  - If the value is a Go function, it will be wrapped in a `BuiltinFunction` that uses `reflect.Value.Call` to execute it. Arguments from the script will be converted from `object.Object` to `reflect.Value` before the call.

- **From minigo to Go: The Unmarshal Bridge**

  The most critical feature of `minigo` is its ability to type-safely unmarshal (or decode) the script's result into a Go struct. This is achieved via the `Result.As(target any)` method, which makes `minigo` a powerful alternative to traditional configuration files.

  ```go
  // --- Go host application code ---
  type MyConfig struct {
      Host string
      Port int
  }

  // Assume 'result' is the successful outcome of a minigo.Run() call
  var cfg MyConfig
  err := result.As(&cfg) // Pass a pointer to populate the struct
  if err != nil {
      // ... handle error
  }
  fmt.Printf("Host: %s, Port: %d\n", cfg.Host, cfg.Port)
  ```

  The `As()` method works as follows, using the `reflect` package:

  1.  It validates that the `result.Value` is a `minigo` internal struct object (`object.StructInstance`), which corresponds to a `{...}` literal returned by the script.
      ```go
      // --- minigo script ---
      return { Host: "localhost", Port: 8080 }
      ```
  2.  It takes the `&cfg` pointer and gets a settable `reflect.Value` of the `MyConfig` struct.
  3.  It iterates through the fields of the `minigo` struct object (e.g., `Host`, `Port`).
  4.  For each field, it looks for a corresponding public field in the Go `MyConfig` struct.
  5.  It checks if the `minigo` object type can be converted to the Go field's type (e.g., `object.String` to `string`, `object.Integer` to `int`).
  6.  If the types are compatible, it uses `reflect` to set the value on the Go struct field (e.g., `field.SetString("localhost")`).

  This mechanism allows `minigo` to function as a type-safe configuration loader, providing a clear advantage over generic scripting engines.

### 3.4. Error Handling and Stack Traces

This will be adapted directly from `minigo`.
- The `Interpreter` will have a `callStack []*CallFrame`.
- A `CallFrame` will store the function name and the position (`token.Position`) of the call site.
- When a function is entered, a frame is pushed to the stack. When it exits, the frame is popped.
- If `eval` encounters an error, it will be wrapped in a custom error type.
- This custom error will have a `Format()` method that iterates through the `callStack` to build a readable trace, including file, line, and column information from the `token.Position`.

## 4. Implementation Phases

The development of `minigo` will be broken down into the following phases, with more detailed and granular tasks.

1.  **Phase 1: Core Interpreter and Expression Evaluation**
    - [ ] Set up the project structure (`minigo/`, `minigo/object/`, `minigo/evaluator/`, etc.).
    - [ ] Define the `object.Object` interface and basic types: `Integer`, `String`, `Boolean`, `Nil`.
    - [ ] Implement the core `eval` loop for expression evaluation.
    - [ ] Support basic literals (`123`, `"hello"`).
    - [ ] Support binary expressions (`+`, `-`, `*`, `/`, `==`, `!=`, `<`, `>`).
    - [ ] Support unary expressions (`-`, `!`).
    - [ ] Write unit tests for all expression evaluations.

2.  **Phase 2: Variables, Constants, and Scope**
    - [ ] Implement the `object.Environment` for managing lexical scopes.
    - [ ] Add support for `var` declarations (e.g., `var x = 10`) and assignments (`x = 20`).
    - [ ] Add support for short variable declarations (`x := 10`).
    - [ ] **Implement `const` declarations**, including typed (`const C int = 1`), untyped (`const C = 1`), and `iota`.

3.  **Phase 3: Control Flow**
    - [ ] Implement `if/else` statements.
    - [ ] Implement standard `for` loops (`for i := 0; i < 10; i++`).
    - [ ] Implement `break` and `continue` statements.
    - [ ] **Implement `switch` statements**:
        - [ ] Support `switch` with an expression (`switch x { ... }`).
        - [ ] Support expressionless `switch` (`switch { ... }`).
        - [ ] Support `case` clauses with single or multiple expressions.
        - [ ] Support the `default` clause.

4.  **Phase 4: Functions and Call Stack**
    - [ ] Implement user-defined functions (`func` declarations).
    - [ ] Implement the call stack mechanism for tracking function calls.
    - [ ] Implement `return` statements (including returning the `nil` object).
    - [ ] Implement rich error formatting with a formatted call stack.

5.  **Phase 5: Data Structures and Pointers**
    - [ ] Add support for `type ... struct` declarations.
    - [ ] Support struct literal instantiation (e.g., `MyStruct{...}`), including both keyed and unkeyed fields.
    - [ ] Support field access (`myStruct.Field`) and assignment (`myStruct.Field = ...`).
    - [ ] Support slice and array literals (`[]int{1, 2}`, `[2]int{1, 2}`).
    - [ ] Support map literals (`map[string]int{"a": 1}`).
    - [ ] Support indexing for slices, arrays, and maps (`arr[0]`, `m["key"]`).
    - [ ] **Implement `for...range` loops** for iterating over slices, arrays, and maps.
    - [ ] **Implement pointer support**:
        - [ ] Define a `Pointer` object type in the object system.
        - [ ] Implement the address-of operator (`&`) to create pointers to variables.
        - [ ] Implement the dereference operator (`*`) to get the value a pointer points to.
        - [ ] Support pointer-to-struct field access (e.g., `ptr.Field`).
        - [ ] Support `new()` built-in function.

6.  **Phase 6: Go Interoperability and Imports**
    - [ ] Create the main `Interpreter` struct that holds a `goscan.Scanner`.
    - [ ] Implement the logic to handle `import` statements and load symbols from external Go packages.
    - [ ] Implement the `object.GoValue` to wrap `reflect.Value`, allowing Go values to be injected into the script.
    - [ ] Implement the logic to wrap Go functions as `BuiltinFunction` objects.
    - [ ] Implement the `Result.As(target any)` method for unmarshaling script results back into Go structs.

7.  **Phase 7: Refinement and Documentation**
    - [ ] Thoroughly test all features, especially pointer handling and the Go interop layer.
    - [ ] Write comprehensive documentation for the API, supported language features, and usage examples.
    - [ ] Ensure `make format` and `make test` pass cleanly.

## 5. Conceptual Usage Example

```go
package main

import (
    "context"
    "fmt"
    "github.com/user/project/minigo"
)

// This is the Go struct we want to populate from the script.
type AppConfig struct {
    ListenAddr string
    TimeoutSec int
    FeatureFlags []string
}

// A Go function we want to make available in the script.
func GetDefaultPort() string {
    return ":8080"
}

func main() {
    // The minigo script, acting as a configuration file.
    script := `
package main

import "strings"

// The entry point function that returns the config.
func GetConfig() {
    // This is the struct we will return.
    // Its definition is not in this script; it will be matched by reflection.
    return {
        ListenAddr: GetDefaultPort(), // Call a Go function from the script.
        TimeoutSec: 30,
        FeatureFlags: strings.Split(env.FEATURES, ","), // Use an injected variable.
    }
}
`
    // The Go variable to inject into the script.
    injectedVars := map[string]any{
        "env": map[string]string{
            "FEATURES": "new_ui,enable_metrics",
        },
        "GetDefaultPort": GetDefaultPort, // Inject the Go function.
    }

    // Run the interpreter.
    result, err := minigo.Run(context.Background(), minigo.Options{
        Source:     []byte(script),
        Filename:   "config.mgo",
        EntryPoint: "GetConfig",
        Globals:    injectedVars,
    })
    if err != nil {
        panic(err) // The error will be nicely formatted with a stack trace.
    }

    // Extract the result into our Go struct.
    var cfg AppConfig
    if err := result.As(&cfg); err != nil {
        panic(err)
    }

    fmt.Printf("Configuration loaded: %+v\n", cfg)
    // Output: Configuration loaded: {ListenAddr::8080 TimeoutSec:30 FeatureFlags:[new_ui enable_metrics]}
}
```

---

## 6. Future Application: Code Generation and IDE Support

While the primary goal of `minigo` is to be a runtime configuration engine, its architecture, particularly its deep integration with `go-scan`, makes it an excellent foundation for more advanced build-time tools, such as a type-safe code generator. This section outlines a potential future goal for `minigo`: powering a tool similar to `examples/convert` while providing a superior developer experience.

### 6.1. Concept

The goal is to allow developers to write mapping logic in a standard `.go` file, have `gopls` provide full autocompletion and type checking for that file, and then use the `minigo` engine to "interpret" that file not for its runtime behavior, but for its declarative structure, to generate code.

A `go:generate` command would trigger the `minigo` tool, which would analyze a special mapping file.

### 6.2. IDE-Friendly Mapping Scripts

The key insight is to **not create a custom language**. By defining the mapping in a valid Go file, we get full tooling support for free.

**Example Mapping Script (`mapping.go`):**

```go
package main

// This file is a "declarative script" for the minigo generator.
// It is never meant to be run directly with 'go run'.

import (
    // Import the user's actual types to make them visible to gopls
    "github.com/my/project/models"
    "github.com/my/project/transport/dto"

    // Import the minigo "verbs" for code generation
    "github.com/podhmo/minigo/generate"
)

func main() {
    // The minigo tool will parse this function's body, but not execute it.
    // It looks for calls to the 'generate' API.
    generate.ConverterFor(
        models.User{}, // Pass a zero value of the type to capture it
        dto.User{},
        generate.Options{
            FieldMap: generate.FieldMap{
                "ID":        "ID",
                "Name":      "FullName",
                "Password":  generate.Ignore(),
            },
        },
    )
}
```

### 6.3. `minigo`'s Role as a Generator Tool

In this mode, the `minigo` tool would:
1.  Parse the `mapping.go` file using `go-scan`.
2.  Walk the AST of the `main` function.
3.  When it finds a call to `generate.ConverterFor`, it analyzes the arguments to understand the user's intent. It can resolve `models.User{}` back to the full `scanner.TypeInfo` for the `User` struct.
4.  It would build an internal code model from this information (just as the `parser` in `examples/convert` does).
5.  This code model is then passed to a `text/template` engine to produce the final, generated `_generated.go` file.

This approach elegantly solves the LSP/IDE support problem. The mapping file is a valid Go program, so `gopls` can provide completions for types (`models.User`), and developers can use "go to definition" and "find usages" as they normally would. The `minigo` tool simply uses this valid Go file as a structured, declarative input for its code generation task. This provides a much better developer experience than using struct tags or custom comment directives.
