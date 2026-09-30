# Trouble Report: `encoding/json` Support in MiniGo

This document outlines the issues encountered while trying to implement and test support for the `encoding/json` standard library package in MiniGo.

## Goal

The goal was to generate bindings for the `encoding/json` package and verify that `json.Marshal` and `json.Unmarshal` could work with a user-defined struct within a MiniGo script.

## Test Case

The following test was added to `minigo/minigo_stdlib_test.go` to verify the functionality.

```go
func TestStdlib_json(t *testing.T) {
	script := `
package main
import "encoding/json"

type Point struct {
	X int
	Y int
}

var p1 = Point{X: 10, Y: 20}
var data, err1 = json.Marshal(p1)

var p2 Point
var err2 = json.Unmarshal(data, &p2)
`
	interp, err := minigo.NewInterpreter()
	if err != nil {
		t.Fatalf("failed to create interpreter: %+v", err)
	}
	stdjson.Install(interp) // stdjson is from "github.com/podhmo/go-scan/minigo/stdlib/encoding/json"

	if err := interp.LoadFile("test.mgo", []byte(script)); err != nil {
		t.Fatalf("failed to load script: %+v", err)
	}
	if _, err := interp.Eval(context.Background()); err != nil {
		t.Fatalf("failed to evaluate script: %+v", err)
	}

    // ... (assertions to check the values of err1, err2, data, and p2)
}
```

## Problem & Error Message

The test failed during the evaluation of the script. The script failed at the `json.Marshal(p1)` call.

The error message from the MiniGo interpreter was:

```
runtime error: argument 1 type mismatch: unsupported conversion from STRUCT_INSTANCE to interface{}
```

## Analysis of Discrepancy

*   **Expected Behavior:** The `json.Marshal` function should accept the MiniGo struct instance `p1`, treat it as a Go `any` (or `interface{}`), and successfully serialize it into a JSON byte slice. Subsequently, `json.Unmarshal` should be able to deserialize the data back into another struct instance.

*   **Actual Behavior:** The MiniGo interpreter's interoperability layer, which handles calls from MiniGo script to native Go functions, does not know how to convert a MiniGo `object.StructInstance` into a Go `interface{}`. The `json.Marshal` function in Go has the signature `func(v any) ([]byte, error)`, and the MiniGo runtime correctly identifies that `p1` (an `object.StructInstance`) is not directly assignable to the `any` type that the Go function expects.

## Missing Features in `go-scan` and `minigo`

This failure highlights a fundamental limitation in the current implementation of MiniGo's foreign function interface (FFI). To fully support packages like `encoding/json`, the following features are needed:

1.  **Bi-directional Type Conversion:** The interpreter needs a robust mechanism to convert values between the MiniGo object system (`minigo/object`) and Go's native types, especially for `interface{}`.
    *   **MiniGo to Go:** When a MiniGo object (like `object.StructInstance`) is passed to a Go function expecting `any`, the interpreter should be able to "unbox" the MiniGo object into a corresponding Go value (e.g., a `map[string]interface{}` or a dynamically created Go struct instance via `reflect`).
    *   **Go to MiniGo:** When a Go function returns a value, the interpreter must "box" it into a corresponding MiniGo object. This part is already implemented for basic types, but it would need to handle complex types like structs and slices returned from functions.

2.  **Pointer and Addressability Support:** The `json.Unmarshal` function requires a pointer to a variable (`&p2`). The MiniGo interpreter needs to be able to handle taking the address of a variable and passing that pointer to a Go function. The Go function would then need to be able to write back to the memory location of the MiniGo variable. This is a complex feature that requires careful memory management.

3.  **Struct Field Tag Support:** For `encoding/json` to be truly useful, the MiniGo struct definition would need to support field tags (e.g., `` `json:"x_coordinate"` ``). The current `go-scan` parser and `minigo` object system do not have a concept of struct field tags. The `json.Marshal` function relies on these tags for customizing the output.

In summary, while the bindings for the functions in `encoding/json` can be generated, their practical use is blocked by the MiniGo runtime's current inability to bridge the gap between its own type system and Go's reflection-based `interface{}` and pointer semantics.

---

## Deeper Analysis and Path to Implementation

Following the initial analysis, a deeper investigation was conducted to determine the feasibility of overcoming these limitations. This analysis is modeled after the one in `sketch/analysis-minigo-goroutine.md`.

### 1. FFI Architecture and a Path for `json.Marshal`

**Current FFI (Foreign Function Interface) Architecture:**
The core of MiniGo's interoperability with Go lies in the `minigo/evaluator/evaluator.go` file.
- **`WrapGoFunction`**: This function wraps a Go function (as a `reflect.Value`) into a `minigo/object.Builtin` object that the interpreter can call.
- **`objectToReflectValue`**: When a Go function is called, this helper function is responsible for converting the MiniGo arguments (`object.Object`) into the `reflect.Value`s that the Go function expects.

**The Point of Failure:**
The investigation confirmed that the failure occurs within `objectToReflectValue`. When it receives a MiniGo `object.StructInstance` and sees that the target Go function ( `json.Marshal`) expects an `interface{}`, it has no defined conversion path and returns the "unsupported conversion" error.

**Proposed Implementation Path for `json.Marshal`:**
Supporting `json.Marshal` is feasible with localized changes. The plan involves enhancing the conversion logic to handle structs:

1.  **Create a Recursive Converter**: Implement or enhance a function, `objectToNativeGoValue(obj object.Object) (any, error)`, that can recursively convert MiniGo objects into their natural Go counterparts. The key addition would be a case for `*object.StructInstance`:
    ```go
    // In minigo/evaluator/evaluator.go
    case *object.StructInstance:
        m := make(map[string]any, len(o.Fields))
        for name, fieldObj := range o.Fields {
            var err error
            // Recursively convert each field
            m[name], err = e.objectToNativeGoValue(fieldObj)
            if err != nil {
                return nil, fmt.Errorf("failed to convert field %q: %w", name, err)
            }
        }
        return m, nil
    ```
    This would turn a MiniGo struct into a `map[string]any`, which `json.Marshal` can handle perfectly.

2.  **Integrate with the FFI**: Modify the `objectToReflectValue` function. When its target type is `interface{}`, it should call the enhanced `objectToNativeGoValue` converter. This bridges the gap for `json.Marshal`.

### 2. Feasibility and Remaining Limitations

| Feature | Feasibility | Notes |
| :--- | :--- | :--- |
| **`json.Marshal(MyStruct)`** | **High** | Feasible with the localized changes described above. Does not require a major architectural redesign. |
| **`json.Unmarshal(data, &MyStruct)`** | **Very Low** | **Not** addressed by the proposed path. `Unmarshal` requires passing a mutable pointer from MiniGo to Go, allowing the Go function to modify MiniGo memory. This is a significant architectural challenge related to memory management and pointer semantics in the FFI, and is considered out of scope for an incremental fix. |
| **Struct Field Tags (`json:"..."`)** | **Low** | **Not** addressed by the proposed path. This would require substantial work in multiple components: updating the `go-scan` parser to recognize and store tags, adding a field for them in the `minigo/object.StructDefinition`, and updating the conversion logic to use them. |

### 3. Conclusion and Recommendation

It is **recommended to proceed** with implementing the proposed path to support `json.Marshal`. This would provide significant value and partially fulfill the goal of `encoding/json` support.

However, it should be clearly understood that this will **not** enable `json.Unmarshal` or custom field naming via tags. Full support for `encoding/json` should be considered a separate, much larger feature that requires a more fundamental redesign of `minigo`'s FFI and memory model.

---

## Path to Full `encoding/json` Support

This section outlines a more detailed plan for achieving more complete `encoding/json` support, based on the analysis above.

### 1. `json.Unmarshal` Runtime Error

The current runtime error for `json.Unmarshal` is a generic "unsupported conversion" message. This can be improved.

*   **Feasibility**: **High**. It is feasible to add a specific check to the FFI logic.
*   **Implementation**: Before calling a Go function, the FFI wrapper can check if the function is `encoding/json.Unmarshal`. If it is, it can inspect the second argument. If the argument is not a pointer to a type it can handle, it can return a more specific error message, such as: "`runtime error: json.Unmarshal is not yet supported because passing mutable pointers to Go functions is not implemented.`" This would provide much better feedback to the user.

### 2. Implementation Plan for `json.Marshal` with Tag Support

This is a more involved feature that requires changes across the `go-scan` stack.

*   **Step 1: Enhance the Parser (`go-scan`)**
    *   **Goal**: Make the scanner aware of struct field tags.
    *   **Action**: Modify the `parseStructType` function in `scanner/scanner.go`. When iterating through `ast.Field`s, read the `field.Tag` property (which is an `*ast.BasicLit`).
    *   The `scanner.FieldInfo` struct already has a `Tag` field. This field should be populated with the string value of the AST tag.

*   **Step 2: Enhance the Interpreter Object Model (`minigo`)**
    *   **Goal**: Allow `minigo` struct definitions to store parsed tag information.
    *   **Action**: The `object.StructDefinition` in `minigo/object/object.go` needs a new field, for example, `FieldTags map[string]string`, which would map a field name to its `json` tag name.
    *   The `evalGenDecl` function in `evaluator.go`, which creates `StructDefinition` objects, must be updated. It will need to iterate over the `ast.Field` list, parse the raw tag string (e.g., `` `json:"my_field,omitempty"` ``), extract the JSON name, and populate the new `FieldTags` map.

*   **Step 3: Enhance the FFI Conversion Logic (`minigo`)**
    *   **Goal**: Use the stored tag information during the conversion to a Go `map`.
    *   **Action**: The `objectToNativeGoValue` function in `evaluator.go` must be modified. When converting a `StructInstance`, it should:
        1.  For each field, look up the corresponding JSON tag name from `StructInstance.Def.FieldTags`.
        2.  If a tag exists, use it as the key for the `map[string]any`.
        3.  If no tag exists, use the original field name as the key.
        4.  (Optional) Add logic to handle tag options like `omitempty`.
