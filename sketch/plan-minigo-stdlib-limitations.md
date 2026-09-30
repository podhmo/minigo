> [!NOTE]
> This feature has been implemented.

# Plan: Standard Library Support Strategies

This document outlines the strategies and findings related to integrating Go's standard library with `minigo`.

## Objective

The primary goal is to expand `minigo`'s standard library support, making it a more powerful and useful scripting engine. This involves finding robust methods for integrating Go packages and understanding the interpreter's capabilities and limitations.

## Strategies and Findings

Two primary methods for integrating standard library packages have been explored, each with its own trade-offs.

### Strategy 1: Direct Source Interpretation (Preferred Method)

This is a new and powerful approach that involves loading the Go source code of a standard library package directly into the `minigo` interpreter at runtime.

- **Process**:
    1. The `goscan.Locator` (with `WithGoModuleResolver`) is used to find the source directory of a stdlib package from the host `GOROOT`.
    2. A new `interpreter.LoadGoSourceAsPackage()` method reads the `.go` files, parses them into an AST, and evaluates the declarations within the interpreter's environment.
    3. This creates a native `minigo` package object that can be imported and used by scripts.

- **Success Case (`slices` package)**: This method was successfully used to implement the `slices` package. This was a significant achievement because `slices` consists entirely of generic functions, which the FFI binding generator (Strategy 2) cannot handle. This approach required enhancing the `minigo` evaluator with several new language features, including variadic arguments (`...`) and full 3-index slice expressions.

- **Advantages**:
    - **Supports Generics**: Bypasses the limitations of the FFI generator, allowing `minigo` to use modern, generic Go code.
    - **Full Compatibility**: Executes the actual Go source, providing high fidelity to Go's semantics (assuming the interpreter supports all required language features).
    - **No Pre-compilation Step**: Integration is done at runtime, simplifying the build process.

- **Limitations**:
    - The `minigo` interpreter must support all Go syntax used in the source file.
    - Does not work for packages that rely on CGO, `unsafe`, or other features that cannot be interpreted.

### Strategy 2: FFI Binding Generation (Fallback Method)

This was the original approach, which uses the `minigo gen-bindings` tool to create a bridge between `minigo` and pre-compiled Go functions.

- **Process**: The tool scans a compiled Go package and generates an `install.go` file that registers package-level functions and constants with the `minigo` interpreter's FFI registry.

- **Advantages**:
    - Works for compiled code and can bridge to packages that use `unsafe` or CGO, as long as the functions being bound do not expose those details in their signatures.

- **Limitations**:
    - **No Generics Support**: The generator fails to create compiling code for generic functions.
    - **No Method Call Support**: The bridge only works for package-level functions. It cannot handle method calls on structs returned from functions (e.g., `(*bytes.Buffer).Write`).
    - **Brittle Error Handling**: The FFI bridge tends to halt the interpreter on Go errors rather than propagating them as `minigo` error objects.
    - **Incomplete Type Support**: Does not recognize `byte` as a built-in alias.

## Recommended Workflow for Stdlib Support

Based on these findings, the following workflow is recommended when adding a new standard library package to `minigo`:

1.  **Attempt Direct Source Interpretation First**: This is the most robust and future-proof method. Create a test case that uses `LoadGoSourceAsPackage` to load the target package and execute a simple function.
2.  **Implement Missing Language Features**: If the test fails, analyze the error to see if it's caused by a missing feature in the `minigo` evaluator (e.g., a specific AST node type is not handled). Prioritize implementing these features.
3.  **Use FFI Binding as a Fallback**: If direct interpretation is not feasible (e.g., the package has CGO dependencies), use the `minigo gen-bindings` tool to create bindings for a curated list of essential, non-generic, package-level functions.

A detailed technical breakdown of the specific limitations encountered with the FFI binding approach is documented in **[`trouble-minigo-stdlib-limitations.md`](./trouble-minigo-stdlib-limitations.md)**.

---

### 3. Testing Strategy

The testing approach was designed to uncover compatibility issues and limitations.

- **Test File**: A new test file, `minigo/minigo_stdlib_custom_test.go`, was created to house all new tests for the generated bindings.
- **Testing Pattern**: Tests follow a consistent pattern:
    1. A minigo script is defined as a Go string.
    2. A new `minigo.Interpreter` instance is created.
    3. The relevant standard library bindings are installed (e.g., `stdbytes.Install(interp)`).
    4. The script is loaded and evaluated by the interpreter.
    5. The state of the interpreter's global environment is checked to assert that the script produced the correct results.
- **Goal-Oriented Testing**: The goal is to test both common use cases and boundary conditions to ensure broad compatibility. The initial testing phase successfully identified several fundamental limitations in the interpreter, which currently block more extensive testing. The tests have been adapted to validate the working functionality while clearly documenting these limitations.

### Analysis of Incompatible Go Features

The investigation into supporting the standard library via direct source interpretation has revealed several fundamental Go features and coding patterns that are incompatible with the current `minigo` interpreter. FFI bindings remain the only viable option for packages that rely on these features.

1.  **CGO, `syscall`, and `unsafe`**:
    -   **Description**: Many core packages (`os`, `net`, `time`) rely on CGO, direct system calls (`syscall`), or the `unsafe` package to interact with the operating system kernel.
    -   **Limitation**: `minigo` is a sandboxed, pure Go interpreter. It has no mechanism to execute C code, make system calls, or perform unsafe memory operations. This is a hard security and implementation boundary.
    -   **Impact**: Any package using these features is fundamentally incompatible with direct source interpretation.

2.  **Sequential Declaration Processing**:
    -   **Description**: In Go, the order of top-level declarations (functions, types, constants, variables) within a single file does not matter. A function can legally reference a type or constant defined later in the file.
    -   **Limitation**: The `minigo` interpreter processes source files sequentially. It fails to resolve identifiers that are used before they are declared. This was observed in packages like `errors`, `strconv`, `path/filepath`, and `encoding/json`.
    -   **Impact**: This is the most common blocker for simple and complex packages alike, as this declaration pattern is widespread in the standard library for organizing code.

3.  **Method Calls on Go Objects**:
    -   **Description**: A common Go pattern is for a function to return a struct, on which the user then calls methods (e.g., `t := time.Now(); year := t.Year()`).
    -   **Limitation**: This was a known limitation of the FFI bridge, but it also applies to direct source interpretation. `minigo` cannot resolve method calls on Go objects that are returned from or passed into the interpreted environment.
    -   **Impact**: Affects a vast number of packages, including `time`, `net/url`, `regexp`, `text/template`, etc.

4.  **Lack of Transitive Dependency Resolution**:
    -   **Description**: Go packages often import other packages to function (e.g., `sort` imports `slices`).
    -   **Limitation**: When `minigo` interprets a source file for a package (e.g., `sort`), it does not automatically interpret the source for the packages it imports (`slices`).
    -   **Impact**: This prevents packages that build on each other from working, requiring a manual and complex process of pre-loading all dependencies.

5.  **Incomplete Language Feature Support**:
    -   **Description**: The standard library uses the full Go language specification.
    -   **Limitation**: `minigo` only implements a subset of the Go language. Key missing features discovered during this investigation include string indexing (`s[i]`) and correct parsing of all function signature variations.
    -   **Impact**: Even simple packages like `strings` fail because they use common language features that `minigo` lacks.

6.  **Complex Reflection (`reflect`)**:
    -   **Description**: Packages like `encoding/json` and `fmt` rely heavily on reflection to inspect and manipulate arbitrary data structures at runtime.
    -   **Limitation**: While many tests failed before hitting reflection-specific code, it is assumed that `minigo`'s support for reflection is insufficient for the complex operations performed in these packages.
    -   **Impact**: Makes packages that provide generic serialization or formatting functionality unsuitable for direct interpretation.
