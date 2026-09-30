# Trouble Report: Investigation into `json.Unmarshal` Error Propagation

This document details the investigation into a bug where `json.Unmarshal` errors were not correctly propagated through the `minigo` FFI, resulting in a `nil` value within the script.

## 1. Summary of Actions

The initial task was to fix a bug noted in `TODO.md`: "*Fix `json.Unmarshal` error propagation: The FFI fails to correctly propagate `*json.UnmarshalTypeError` from `json.Unmarshal`, returning a `nil` value instead.*"


---


After re-evaluating the problem from first principles, a critical misunderstanding in the initial analysis was discovered. The premise of the entire investigation was flawed.

**The Flawed Premise:** I assumed that calling `json.Unmarshal` from `minigo` would trigger a `*json.UnmarshalTypeError` in the same way it does in native Go.

**The Reality:** The FFI bridge for pointer arguments (specifically for interfaces) does not pass a pointer to a Go equivalent of the `minigo` struct. Instead, it creates a **pointer to a `map[string]any`** and passes that to `json.Unmarshal`.

This can be seen in `minigo/evaluator/evaluator.go` inside `WrapGoFunction`:
```go
if ptr, isPtr := arg.(*object.Pointer); isPtr && targetType.Kind() == reflect.Interface {
    var nativePtr any
    underlying := *ptr.Element
    if _, ok := underlying.(*object.StructInstance); ok {
        var m map[string]any // <-- A new map is created
        nativePtr = &m       // <-- A pointer to the map is used
    }
    // ...
    in[i] = bridge.Dest // bridge.Dest is the pointer to the map
}
```

**Why this is the root cause:**
The Go `encoding/json` package, when unmarshaling into a value of type `any` (or `interface{}`), which is what a pointer to a map becomes, **does not perform struct field type checking**. It happily unmarshals the JSON into a map representation. The string `"not-a-number"` is simply placed into the map as a string value for the key `"Age"`.

Therefore, `json.Unmarshal` **never returns an error in the test case**. It successfully unmarshals the data into the temporary map. The returned `error` value is genuinely `nil`.

The subsequent logic in the FFI bridge then attempts to copy the values from this temporary map into the actual `minigo` struct instance. This copy operation is where a type mismatch *could* occur, but the current implementation (`updateMiniGoStructFromNative`) does not do any type validation; it simply assigns the values.

**Conclusion:**
The bug is not that a non-nil error is being converted to `nil`. The bug is that **no error is ever generated in the first place** because of the FFI's use of an intermediate `map[string]any` which bypasses Go's own type validation for `json.Unmarshal`. The test fails because it *expects* an error that can never happen with the current FFI architecture.

To fix this, the FFI bridge would need to be fundamentally changed to dynamically create a real Go struct (using `reflect`) that matches the `minigo` struct definition, and pass a pointer to that Go struct to `json.Unmarshal`. This would allow Go to perform the type checking and return the expected `*json.UnmarshalTypeError`.

## 5. Implementation Efforts and Obstacles

Based on the root cause analysis, a new plan was formulated to fix the bug correctly:

1.  **Refactor the FFI Pointer Bridge:** The core of the fix was to replace the `map[string]any` logic with one that dynamically builds a Go struct mirroring the `minigo` struct, using `reflect.StructOf`.
2.  **Create Helper Functions:** This required new helpers, `minigoTypeToGoType` and `buildGoStructType`, to translate `minigo` type definitions into `reflect.StructField` slices.
3.  **Update FFI Call Sites:** The logic in `WrapGoFunction` and `evalGoValueSelectorExpr` needed to be updated to call these new helpers, instantiate the dynamic struct, and pass a pointer to `json.Unmarshal`.
4.  **Update Copy-Back Logic:** The function for copying data back from the FFI call (from the Go struct to the `minigo` struct) also needed to be rewritten.

### Obstacle: `undefined: fscope`

I proceeded to implement this new plan. However, the implementation was blocked by a recurring and difficult-to-solve build error: `undefined: fscope`.

-   **Context:** The new `buildGoStructType` helper function needs to resolve the type of each struct field. To do this, it calls `e.Eval(field.Type, env, fscope)`. This requires the `fscope` (`*object.FileScope`) to correctly resolve type names that might belong to other packages (e.g., `pkg.MyType`).
-   **The Problem:** The `fscope` is readily available in some parts of the evaluator, but not within the closure of the `*object.Builtin` `Fn` that is created inside `evalGoValueSelectorExpr`. This function is created and returned, and the `fscope` from its creation context is lost by the time the `Fn` is executed.
-   **Attempts to Fix:**
    1.  I attempted to pass the `fscope` down into `WrapGoFunction` and the `evalGoValueSelectorExpr` builtin.
    2.  This led to further complications, as it required adding the `fscope` to the `BuiltinContext`, which created a tangled dependency.
    3.  My attempts to correctly thread the `fscope` through the call stack failed, leading to repeated build errors.

After multiple failed attempts to resolve this architectural challenge, I was unable to complete the implementation. The proposed plan remains correct, but requires a more sophisticated way to handle scope and context within the FFI wrapper functions.
