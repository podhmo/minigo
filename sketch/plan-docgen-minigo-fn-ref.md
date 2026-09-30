# Plan: Implementing Type-Safe `docgen` Patterns via a Robust Library

## 1. Objective

The primary goal is to refactor `docgen`'s custom pattern configuration to be type-safe and developer-friendly. This has been achieved by replacing string-based keys (e.g., `Key: "my/pkg.MyFunc"`) with direct function and method references (e.g., `Fn: mypkg.MyFunc`, `Fn: (*mypkg.MyType)(nil).MyMethod`) in the `minigo` configuration script.

A core principle of this task was to **enhance the robustness of the underlying `go-scan` and `minigo` libraries**. The goal was not to find test-specific workarounds but to improve the libraries so they can correctly handle complex, real-world Go module structures, such as the nested modules often used in testing environments. This was successfully validated using the `scantest` library.

## 2. Final Implementation

The `minigo` interpreter was enhanced to correctly represent Go functions and methods as first-class objects that can be referenced as values.

-   **`minigo` Object Model (`minigo/object/object.go`)**:
    -   A `GoSourceFunction` object was implemented to represent a Go function reference. This object stores the function's metadata, its full package path, and its definition environment (`DefEnv`).
    -   A `GoMethodValue` object was implemented to represent a method resolved from a typed `nil` pointer (e.g., `(*MyType)(nil).MyMethod`).
    -   The existing `BoundMethod` object is now also handled by `docgen`'s loader to support method references on live instances (e.g., `var v MyType; v.MyMethod`).

-   **`minigo` Evaluator (`minigo/evaluator/evaluator.go`)**:
    -   The evaluator's selector logic (`evalSelectorExpr`) was updated to handle method references on typed `nil` pointers, creating `GoMethodValue` objects.
    -   The symbol resolution logic (`findSymbolInPackage`) was enhanced to proactively attach all of a struct's methods when its `StructDefinition` is created, ensuring methods are available for resolution.
    -   The `minigo` unmarshaler (`minigo.go`) was updated to recognize and handle `GoMethodValue` and `BoundMethod` objects when converting script results back to Go structs.

-   **`docgen` Feature Implementation (`examples/docgen/loader.go`)**:
    -   `convertConfigsToPatterns` was updated to process the `Fn` field. It now inspects `GoSourceFunction`, `GoMethodValue`, and `BoundMethod` objects and uses their properties (package path, receiver type, name) to dynamically construct the internal matching key.

## 3. Testing Strategy

The testing strategy was to validate the library's robustness using the `scantest` library, which creates isolated, in-memory test modules.

-   **`docgen` Key-Generation Test (`examples/docgen/key_from_fn_test.go`)**:
    -   A new, lightweight test was created specifically to verify the key generation logic.
    -   It uses `scantest` to create a self-contained test module with a `go.mod` file that includes a `replace` directive, proving the module resolution is robust.
    -   The test defines a `patterns.go` file with various `Fn` references (standalone function, method from typed nil, method from value instance, method from pointer instance) and asserts that `docgen`'s loader generates the correct fully-qualified key for each one.

## 4. High-Level Task List

-   **Core Library Robustness**:
    -   [x] **Enhance Module Resolution**: Fix the `go-scan` locator to correctly resolve packages in nested test modules with `replace` directives. (Validated with `scantest`).
    -   [x] **Implement Typed Nil Method Values**: Enhance the `minigo` interpreter to support resolving method values from typed `nil` pointers.
    -   [x] **Implement Environment-Aware Function Objects**: Enhance `minigo` to represent Go functions as objects that retain their definition environment (`DefEnv`).
    -   [x] **Support Instance Method Values**: Enhance `docgen` to resolve method references from live object instances.

-   **`docgen` Feature**:
    -   [x] **Refactor `docgen` Configuration**: Update `PatternConfig` to use a type-safe `Fn` field instead of a string `Key`.
    -   [x] **Implement Key Computation**: Update the `docgen` loader to dynamically generate the matching key from the `Fn` reference for functions, typed-nil methods, and instance methods.

-   **Verification**:
    -   [x] **Validate with Integration Test**: Create and pass a `docgen` test that uses a nested Go module created with `scantest`, proving the module resolution and the `docgen` feature work together correctly.
