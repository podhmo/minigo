# Trouble Report: `minigo` Package Resolution Failure in Nested Test Modules

This document provides a detailed analysis of a persistent package resolution failure encountered when using the `minigo` interpreter within a `go test` environment that involves multiple, nested Go modules.

## Goal

The high-level goal was to add a feature to `docgen` that used `minigo` to load a configuration file. This configuration file needed to import a package from within its own test-specific Go module.

## The Problematic Structure

The core of the issue lies in the interaction between three different Go modules during a test run.

**1. Directory Layout:**

```
/ (root of go-scan project)
├── go.mod (Module: github.com/podhmo/go-scan)
│
└── examples/
    └── docgen/
        ├── go.mod (Module: github.com/podhmo/go-scan/examples/docgen)
        │
        ├── main_test.go (The test being executed)
        │
        └── testdata/
            └── fn-patterns/
                ├── go.mod (Module: github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns)
                │
                ├── patterns.go (The minigo script being evaluated)
                │
                └── api/
                    └── api.go (The package that patterns.go tries to import)
```

**2. Module Definitions:**

- **Root `go.mod`**: `module github.com/podhmo/go-scan`
- **`docgen` `go.mod`**: `module github.com/podhmo/go-scan/examples/docgen`
  - Contains `replace github.com/podhmo/go-scan => ../../`
- **`fn-patterns` `go.mod`**: `module github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns`
  - Contains `replace github.com/podhmo/go-scan => ../../../../`

**3. Code Interaction:**

- **`main_test.go`** (in `docgen` module) runs a test, `TestDocgen_withFnPatterns`.
- This test calls `docgen`'s `LoadPatternsFromConfig` function.
- `LoadPatternsFromConfig` creates a `goscan.Scanner` configured to use the `fn-patterns` directory as its working directory. This scanner is then passed to the `minigo` interpreter.
  ```go
  // in main_test.go
  moduleDir := "testdata/fn-patterns"
  scanner, err := goscan.New(goscan.WithWorkDir(moduleDir))
  // ...
  interp, err := minigo.NewInterpreter(scanner)
  // ...
  ```
- The `minigo` interpreter then evaluates **`fn-patterns/patterns.go`**.
- **`fn-patterns/patterns.go`** tries to import the `api` package:
  ```go
  // in patterns.go
  import "github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns/api"
  ```
- The `api` package is defined in **`fn-patterns/api/api.go`** as `package api`.

## The Failure

When `go test` is run from the `examples/docgen` directory, the `minigo` interpreter fails to evaluate `patterns.go`.

**Exact Error Message:**
```
failed to load custom patterns: failed to evaluate patterns config source: runtime error: undefined: api.API (package scan failed: could not get unscanned files for package github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns/api: could not find package directory for "github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns/api" (tried as path and import path): import path "github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns/api" could not be resolved. Current module is "github.com/podhmo/go-scan/examples/docgen/testdata/fn-patterns" (root: /app/examples/docgen/testdata/fn-patterns))
```

### Analysis of Failure

The error message is contradictory and reveals the core of the problem.
- It correctly identifies the `Current module` as `.../fn-patterns` with the correct root directory. This indicates that the `WithWorkDir` option is working as intended.
- However, it then claims it `could not find package directory` for an import path that should be resolvable *within that exact module*.

This strongly suggests that the `go-scan/locator` component, when invoked by a `minigo` interpreter that itself is running inside a `go test` process from a *different* parent module, is unable to correctly layer the filesystem and module contexts. The `go test` process's primary module context (`examples/docgen`) seems to be interfering with the `go-scan` locator's ability to resolve packages within the nested test module (`fn-patterns`), even when explicitly told that the nested module is its workspace.

## Chronicle of Failed Attempts

1.  **Changing Test Execution Directory**: Running `go test` from `/` vs. `/examples/docgen` made no difference. The pathing errors changed, but the fundamental resolution failure remained.
2.  **Simplifying Module Structure**: I removed the `fn-patterns/go.mod` file and treated `fn-patterns` as a standard Go package within the `docgen` module. This failed because the `patterns.go` script needs to import `github.com/podhmo/go-scan/examples/docgen/patterns`, which now creates a circular dependency within the `docgen` module. The submodule approach is necessary to break this cycle.
3.  **Isolating the Test**: Moving the test logic to a separate `*_test.go` file had no effect.
4.  **Verifying `minigo` Standalone**: The `minigo` interpreter's own test suite passes flawlessly, including tests for `replace` directives. This confirms the issue is not with `minigo` in isolation, but with its use in this specific nested test context.

## Resolution

This issue has been **resolved**. The root cause was not in the locator itself, but in how the `minigo.Interpreter` was constructed. Previously, the interpreter created its own internal `goscan.Scanner`, which did not correctly inherit the working directory context provided by the test.

The fix involved changing the `minigo.NewInterpreter` signature to **require** a pre-configured `*goscan.Scanner`. This ensures that the caller is responsible for creating and configuring the scanner with the correct working directory, and the interpreter then uses that scanner instance. This change makes the dependency explicit and resolves the context-switching problem.

The code snippet in the "Problematic Structure" section has been updated to reflect the new, correct usage.

A better test strategy would be:
1.  In `main_test.go`, read the contents of `api.go` and `patterns.go` into strings.
2.  Create a `minigo` interpreter.
3.  Use `minigo.LoadGoSourceAsPackage("api", apiSource)` to manually load the `api` package into the interpreter's memory. This bypasses the filesystem locator.
4.  Use `minigo.EvalString(patternsSource)` to evaluate the patterns script. Since the `api` package is now in memory, the import should resolve.

This creates a hermetic test for the `minigo` evaluation and `docgen` loading logic without depending on the fragile nested module resolution via the filesystem.

---

## Follow-up Verification (2024-08)

A follow-up investigation was conducted to reproduce the original scenario using a full integration test, rather than a hermetic one. The goal was to confirm that the `go-scan` locator and `scantest` library correctly handle the nested module `replace` scenario in a real-world context.

An integration test was added in `examples/docgen/integration_test.go` (`TestDocgen_WithFnPatterns`). The process revealed several key insights:

1.  **Feature Status**: The original `minigo` script (`patterns.go`) attempted to use a function reference (`api.GetFoo`) as a map key. The current implementation of `docgen`'s loader expects a string literal. The test script had to be updated to provide the fully-qualified function name as a string to match the existing functionality. This confirmed that the feature to use function references as keys (tracked in `TODO.md`) is not yet complete.

2.  **`replace` Path Complexity**: The test setup involved a nested module at `testdata/integration/fn-patterns/`. Getting the relative paths in the `replace` directives correct was crucial and required careful verification. The final, working `go.mod` for the nested module needed to replace both the `docgen` module and the root `go-scan` module with the correct number of `../` segments.

3.  **Test Data Separation**: Initially, the test was written with file contents defined inline using `scantest.WriteFiles`. It was later refactored to use external files in the `testdata/integration` directory. This improved the test's clarity by separating the test logic from the test data.

Ultimately, the test was made to pass, confirming that the core module resolution mechanism is robust and correctly handles nested modules with multiple `replace` directives when configured properly.
