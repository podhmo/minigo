> [!NOTE]
> This issue has been resolved. The minigo interpreter now correctly instantiates variables of FFI-provided types as `*object.GoValue` wrappers, allowing reflection-based method calls to succeed. This document is preserved for historical context.

# Problem: Method Calls on In-Script Instances of FFI Structs

This document details a fundamental architectural issue in the `minigo` interpreter concerning the boundary between its native object system and Go types imported via the FFI (Foreign Function Interface).

## 1. The Problem

The interpreter fails to resolve method calls on pointers to struct instances when the struct type is from a Go package (via FFI) but the instance itself is created within the script (e.g., `var s scanner.Scanner`).

This was discovered while trying to enable the FFI-based test for the `text/scanner` package.

### Failing Code Example

The following `minigo` script fails:

```go
package main

import (
	"strings"
	"text/scanner"
)

func main() {
	var src = strings.NewReader("hello world 123")

	// 's' is a zero-valued struct instance created in the script's environment.
	// Its type 'scanner.Scanner' comes from the FFI.
	var s scanner.Scanner

	// 'p' is a pointer to the in-script struct instance.
	var p = &s

	// This method call fails.
	p.Init(src)
}
```

### Observed Error

The script fails with the following error, originating from the `p.Init(src)` call:

```
runtime error: undefined field or method 'Init' on pointer to struct 'Scanner'
```

This indicates that the interpreter cannot find the `Init` method on the `*scanner.Scanner` type when the instance is created and manipulated this way.

## 2. Root Cause Analysis: A Tale of Two Object Systems

The issue stems from how `minigo` represents values. There are two distinct kinds of objects involved:

1.  **Native `minigo` Objects (`*object.StructInstance`)**: When a struct is defined *inside* a `minigo` script, the interpreter creates a `*object.StructDefinition` and populates its `Methods` map by parsing the AST. Instances of these structs are `*object.StructInstance`. Method calls on these are resolved by looking up the method name in the `Methods` map.

2.  **Wrapped Go Objects (`*object.GoValue`)**: When a native Go function is called via the FFI and it returns a value (like a `*bytes.Buffer` from `bytes.NewBuffer`), that value is wrapped in a `*object.GoValue`. This object holds a `reflect.Value`. Method calls on a `*object.GoValue` are dispatched to `evalGoValueSelectorExpr`, which uses Go's `reflect` package (`val.MethodByName(sel)`) to find and call the method. This is powerful and works correctly for both value and pointer receivers.

The failure occurs at the intersection of these two systems.

-   When the script executes `var s scanner.Scanner`, the `scanner.Scanner` type is resolved through the FFI registry. However, the interpreter creates a **`*object.StructInstance`** for the variable `s`.
-   This `*object.StructInstance` is just a placeholder. Its `Def` (`*object.StructDefinition`) does not have its `Methods` map populated because the interpreter did not parse the source code of the `text/scanner` package; it only knows the type name exists.
-   When `p.Init()` is called, the `evalSelectorExpr` logic for `*object.Pointer` correctly finds the `*object.StructInstance`, but when it looks for the `Init` method in the `Methods` map, it's not there.
-   The evaluator has no fallback mechanism to use reflection on this `*object.StructInstance` because it lacks the `reflect.Type` information associated with `scanner.Scanner`.

In contrast, if `scanner.Scanner` were returned from a Go function (e.g., `s := scanner.New()`), it would be a `*object.GoValue`, and `s.Init()` would work perfectly via the reflection-based path.

## 3. Current Implementation

The relevant logic is in `minigo/evaluator/evaluator.go`:

-   **`evalGenDecl`**: For a `var s T` declaration, if `T` is an FFI type, it creates a `*object.StructInstance` with an empty (or zero-valued) `Fields` map. It does not create a `*object.GoValue`.
-   **`evalSelectorExpr`**: The `case *object.Pointer:` block handles method resolution. It tries to look up the method in the `instance.Def.Methods` map. This fails for FFI types instantiated in-script, as the map is empty. There is no fallback to reflection.
-   **`evalGoValueSelectorExpr`**: This function, which is *not* triggered for these objects, correctly uses `reflect.Value.MethodByName` to resolve methods.

## 4. Potential Solutions

Two main strategies were considered to resolve this.

### Solution A: Change FFI Type Instantiation (Recommended)

This approach tackles the root cause: the incorrect representation of in-script instances of FFI types.

-   **Proposal**: Modify `evalGenDecl`. When evaluating `var s T`, if `T` is identified as an FFI-provided Go type, instead of creating a `minigo` `*object.StructInstance`, the interpreter should create a `*object.GoValue` that wraps a zero-valued instance of the actual Go type (e.g., `reflect.Zero(goType)`).
-   **Pros**:
    -   Architecturally clean. It ensures that values associated with Go types are consistently represented by `*object.GoValue`, regardless of whether they are returned from an FFI function or instantiated in-script.
    -   Leverages the existing, working reflection-based method call mechanism in `evalGoValueSelectorExpr`.
-   **Cons**:
    -   Requires a reliable way to distinguish between `minigo`-defined types and FFI-provided types within `evalGenDecl`.
    -   The `*object.Pointer` logic would need to be updated to correctly handle pointers to `*object.GoValue` for method calls and field assignments. This is a non-trivial change.

### Solution B: Add Reflection Fallback to `StructInstance` (Hackier)

This approach attempts to patch the existing method lookup logic.

-   **Proposal**: Modify `evalSelectorExpr`. In the `case *object.Pointer:` block, if the method lookup in the `Methods` map fails, add a fallback path. This path would attempt to resolve the `*object.StructInstance` back to a `reflect.Type`, create a `reflect.Value` from it, and then use the reflection-based logic from `evalGoValueSelectorExpr`.
-   **Pros**:
    -   More surgically targeted to the point of failure.
-   **Cons**:
    -   Feels like a hack. It further blurs the line between the two object systems.
    -   It's difficult to implement cleanly because the `*object.StructDefinition` does not currently store a reference to the `reflect.Type` it represents, so there's no easy way to get the type information needed for reflection. Adding this link would be a significant change in itself.

Given the analysis, **Solution A** appears to be the more robust and correct long-term solution, despite its complexity. It addresses the core representational inconsistency.
