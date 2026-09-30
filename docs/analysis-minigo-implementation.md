# Analysis of `minigo` Implementation

`minigo` is a simple, AST-walking interpreter for a subset of the Go language. While it functions as a classic interpreter for control flow and expressions, its most significant and powerful feature is its **dynamic and lazy import resolution**, which is fundamentally powered by its underlying dependency, the `go-scan` library.

Unlike a standard Go compiler, which statically links all dependencies upfront, `minigo` defers all package resolution until a symbol is actually accessed at runtime.

This document analyzes this core mechanism and explains how it enables `minigo`'s primary use-case: dynamically leveraging parts of a complex Go codebase, such as type definitions, as if they were simple, self-contained objects (POGO - Plain Old Go Objects), without needing to resolve their entire dependency tree.

## The Core Mechanism: Dynamic and Lazy Import Resolution

The most important feature of `minigo` is how it handles dependencies. This behavior is a direct result of the "shallow, lazy scanning" philosophy of the underlying `go-scan` library.

### 1. `import` is a No-Op (Initially)

In `minigo`, an `import "path/to/pkg"` statement does not trigger any immediate scanning or parsing of the imported package. The interpreter simply records the alias and path. This stands in stark contrast to a Go compiler, which would need to locate, parse, and type-check the imported package and its own dependencies at compile time.

### 2. Resolution is Triggered by Access

The import resolution logic is deferred until a symbol from the package is actually accessed in the code. When the evaluator encounters a selector expression like `pkg.MyType`, it triggers the `findSymbolInPackage` function. Only at this moment does `minigo` ask `go-scan` to find the definition for `MyType` in `path/to/pkg`.

### 3. `go-scan` Performs a Targeted, Shallow Scan

Crucially, `go-scan` does not perform a deep, recursive analysis of the entire `path/to/pkg` and all of its dependencies. Instead, it performs a targeted scan, parsing just enough of the source code to locate the AST for `MyType`. If `MyType` has fields whose types are from other packages, `go-scan` does not immediately resolve them either. It simply records their type information, deferring their resolution until they are, in turn, accessed.

### 4. The Key Use-Case: Using Legacy Types as POGO

This lazy, on-demand, and shallow resolution is what makes `minigo` exceptionally powerful for working with existing, complex codebases.

Consider a large, legacy Go project with a monolithic `models` package. This package might have hundreds of structs with tangled dependencies on other internal packages for services, validation, or database logic. In a standard Go environment, using a single struct from `models` requires the compiler to successfully build the entire dependency graph of the `models` package. If any part of that graph is broken or has complex build requirements, using the struct becomes difficult.

`minigo` completely bypasses this problem. Because it only resolves what is explicitly accessed, a `minigo` script can do the following:

```go
import "my-legacy-app/models"

func GetConfig() {
    return models.User{
        Name: "Default User",
    }
}
```

When `minigo` interprets this, it only asks `go-scan` to find the definition of `models.User`. It never attempts to parse or understand the other structs, methods, or dependencies within the `models` package. As a result, the complex `models.User` type, with all its potentially problematic dependencies, can be treated as a simple **Plain Old Go Object (POGO)**.

This allows developers to treat complex, existing Go types as simple data structures, effectively repurposing them as schemas for configuration files without paying the cost of their full dependency hierarchy. It enables an ad-hoc, bottom-up approach to leveraging existing code that is impossible with a static, top-down compiler.

## Other Applications

While treating legacy types as POGO is a key outcome, this core mechanism enables other powerful use-cases.

-   **Go as a Configuration Language (`examples/docgen`)**: This is a direct application of the POGO principle. The host application (`docgen`) asks `minigo` to execute a user's script and return a configuration object. `minigo`'s ability to dynamically interpret the script and unmarshal the resulting structure into a native Go struct, without a compile step, makes it a powerful configuration engine.

-   **AST Manipulation with Special Forms (`examples/convert-define`)**: `minigo` can register "special forms," which are functions that receive unevaluated AST nodes instead of values. This allows `minigo` to be used as a meta-programming or code-generation tool, where a script can define a DSL (Domain-Specific Language) that is then transformed into Go code by the host application. This works seamlessly because `minigo`'s parser accepts any valid Go syntax, even for functions (the special forms) that have no real implementation in the Go code itself, allowing tools like `gopls` to function correctly.

## Conclusion

`minigo` is more than just a simple interpreter for a subset of Go. Its true value lies in its **dynamic and lazy import mechanism**, inherited from `go-scan`. This design choice elevates it from a mere script runner to a powerful tool for bridging the gap between Go's static, compiled world and the need for dynamic, ad-hoc code evaluation.

By intentionally avoiding a full, upfront dependency resolution, `minigo` provides a unique and pragmatic capability: the ability to surgically extract and reuse pieces of a complex, legacy Go codebase as simple POGO definitions. This makes it an exceptionally effective engine for building powerful, Go-native configuration systems and lightweight, meta-programming tools.
