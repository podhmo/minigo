# Analysis of a REPL for `minigo`

## 1. Introduction

This document analyzes the feasibility and potential implementation strategies for adding a Read-Eval-Print Loop (REPL), or interactive shell, to the `examples/minigo` interpreter. The goal is to provide a more interactive way to experiment with the `minigo` language, which currently operates as a batch script executor.

This analysis will explore three distinct approaches:
1.  A **Simple REPL**: A basic, line-by-line interactive console.
2.  A **Rich REPL**: An enhanced user experience using a terminal UI library like `bubbletea`.
3.  **Interpreter-Specific Features**: Adding meta-commands to the REPL for introspection and environment manipulation.

The document will examine the existing architecture, discuss the implementation details and complexity of each approach, and conclude with a recommendation for a path forward.

## 2. Current Architecture

The `minigo` interpreter is currently a non-interactive, file-based executor. A user runs it from the command line, providing a path to a `.minigo` script file. The interpreter then performs the following steps:

1.  **Initialization**: In `examples/minigo/main.go`, a `minigo.Interpreter` instance is created. This object, defined in `minigo/minigo.go`, serves as the core of the runtime, holding the execution state.
2.  **State Management**: The `Interpreter` struct contains a persistent environment (`globalEnv`), a package cache, a symbol registry for Go interop, and configurable I/O streams (`stdin`, `stdout`, `stderr`). This stateful design is crucial and well-suited for a REPL, as it allows variables, functions, and imports to persist between executions.
3.  **File Loading**: The entire script file is read into memory.
4.  **Evaluation**: The script's content is passed to `interp.LoadFile()` and then `interp.Eval()`. The evaluation itself is handled by the `Evaluator` struct (`minigo/evaluator/evaluator.go`), which is a classic AST-walking interpreter. It recursively traverses the code's syntax tree and executes it sequentially.
5.  **Output**: The final result of the script's `main()` function (or the last expression) is printed to standard output, and the program exits.

This architecture is fundamentally single-threaded and processes one file in its entirety. There is no mechanism for interactive input or for preserving the interpreter's state across multiple, separate user inputs.

## 3. Approach 1: A Simple REPL

This approach focuses on creating a minimal, functional REPL with the fewest changes required. It would provide a classic interactive prompt.

### 3.1. Implementation Details

A simple REPL can be implemented by modifying the `main` function in `examples/minigo/main.go`. Instead of reading a file, it would enter an infinite loop.

```go
// main.go
func main() {
    interp, err := minigo.NewInterpreter()
    // ... register builtins ...

    scanner := bufio.NewScanner(os.Stdin)
    fmt.Print(">> ")

    for scanner.Scan() {
        line := scanner.Text()
        if line == "exit" {
            break
        }

        // How to evaluate a single line?
        // This is the main challenge. We cannot just call interp.Eval()
        // as that looks for a `main` function.

        fmt.Print(">> ")
    }
}
```

The core challenge is that the existing `Interpreter.Eval()` is designed to evaluate a whole file and then run its `main` function. For a REPL, we need to parse and evaluate one line at a time while maintaining the state in `interp.globalEnv`. This requires a new method on the `Interpreter`, such as `EvalString(input string)`.

This new method would:
1.  Parse the input string into an AST node using `parser.ParseFile`.
2.  Iterate through the declarations (`Decls`) in the parsed file.
3.  Call `e.eval.Eval(node, e.globalEnv, fileScope)` for each node.
4.  Inspect the result of the last evaluated expression and print it.

### 3.2. Pros and Cons

*   **Pros**:
    *   Relatively simple to implement.
    *   Reuses the vast majority of the existing `Interpreter` and `Evaluator` logic.
    *   No new external dependencies are required.
*   **Cons**:
    *   Poor user experience: no command history, no syntax highlighting, no multi-line editing.
    *   Error handling would be basic, likely printing the error and restarting the loop.
    *   Handling multi-line statements (like function or struct definitions) would be difficult.

### 3.3. Required Core Library Changes

The core `minigo` library is missing a key feature for a REPL: the ability to evaluate a single string of input.

*   **`minigo.Interpreter` needs a new method**: A new public method, for example `EvalString(ctx context.Context, input string) (object.Object, error)`, is required.
    *   The current `Eval()` method is designed to evaluate a set of pre-loaded files and find a `main` function.
    *   The new `EvalString()` method would need to take a string, parse it into an AST, evaluate all statements within it against the interpreter's persistent `globalEnv`, and return the `object.Object` result of the final expression. This allows the REPL to print the result of something like `1 + 1` or show `nil` for an assignment like `x := 10`.

## 4. Approach 2: A Rich REPL with Bubble Tea

This approach focuses on building a modern, user-friendly REPL using the `github.com/charmbracelet/bubbletea` library.

### 4.1. Implementation Details

Instead of a simple `bufio.Scanner` loop, we would build a `bubbletea` application. The `bubbletea` model would manage the REPL's state, including the input line, command history, and the `minigo.Interpreter` instance.

Key features would include:
*   **Command History**: Store each submitted line in a slice and allow the user to navigate it with up/down arrow keys.
*   **Multi-line Input**: The text input component from `bubbletea` can easily handle multi-line input, making it trivial to define functions or structs interactively.
*   **Syntax Highlighting**: While not built-in, `bubbletea` can be integrated with syntax highlighting libraries to provide real-time feedback.
*   **Autocompletion**: A more advanced feature where pressing `Tab` could suggest completions. This is a complex feature that warrants a more detailed breakdown.

#### 4.1.1. A Deep-Dive into Autocompletion

A robust autocompletion system would need to handle several different contexts:

*   **Global Symbol Completion**: When the user starts typing a word in an empty context (e.g., `my_v`), the REPL should suggest matching symbols from the current scope. This would include user-defined variables, functions, built-in functions (`len`, `append`), and even meta-commands (`:help`).
*   **Package Member Completion**: After the user types a package alias and a dot (e.g., `fmt.`), the REPL should suggest exported members of that package (`Println`, `Sprintf`, etc.).
*   **Struct Field and Method Completion**: After the user types a variable that holds a struct and a dot (e.g., `myPerson.`), the REPL should suggest the fields and methods of that struct (`Name`, `Age`, `SayHello`).

Implementing this requires the REPL to have a deep, contextual understanding of the code being typed.

### 4.2. Pros and Cons

*   **Pros**:
    *   Dramatically improved user experience.
    *   Provides a solid framework for adding more advanced features in the future.
    *   Handles complex UI interactions gracefully.
*   **Cons**:
    *   Introduces a new major dependency (`bubbletea` and its ecosystem).
    *   Significantly more complex to implement than the simple REPL.
    *   Requires learning the `bubbletea` framework (The Elm Architecture).

### 4.3. Required Core Library Changes

In addition to the `EvalString()` method needed for the simple REPL, a rich REPL with advanced autocompletion requires significant introspection capabilities.

*   **For global symbol completion**:
    *   **`minigo.Interpreter` needs a symbol listing method**: To suggest completions for variables and functions in the global scope, a public method like `ListSymbols() []string` is required. This method would inspect the `globalEnv` and return a slice of all defined symbol names.

*   **For contextual completion (struct fields, package members)**:
    *   **`minigo.Interpreter` needs a type analysis method**: This is the most complex requirement. For the REPL to suggest struct fields (e.g., after a user types `myVar.`), it must be able to determine the type of `myVar`. This requires a new, advanced method on the interpreter, such as `GetTypeInfoOf(expression string) (*TypeInfo, error)`.
    *   This method would need to parse the partial input string (the expression), evaluate it in a temporary/analytical context to get the resulting `object.Object`, and then inspect that object's type definition to return a list of its members (fields and methods). This is a non-trivial feature that turns the interpreter into a static analysis tool on the fly.
    *   The `go-scan` changes proposed in Approach 3 (i.e., `ScanPackageFromFilePath`) would also be essential for completing members of imported packages.

## 5. Approach 3: Interpreter-Specific Features

This approach is orthogonal to the UI and focuses on adding powerful "meta-commands" to the REPL that are not part of the `minigo` language itself. These commands would be intercepted by the REPL loop before being sent to the interpreter.

### 5.1. Implementation Details

The REPL loop (whether simple or `bubbletea`-based) would check if the input line starts with a special prefix, such as a colon (`:`).

*   **:help**: Display a list of available meta-commands.
*   **:symbols** or **:ls [pkgpath]**: Inspect the `interp.globalEnv` and print a list of all user-defined variables and functions. If a package path is provided, it lists the symbols in that package instead.
*   **:cd <pkgpath>**: Functions like `cd` in a shell, setting the "current package" context for the REPL. Subsequent commands like `:ls` would then operate on this package context by default. Calling `:cd` with no arguments would return to the global (user-defined) context.
*   **:pkg <package_path>**: (Superseded by `:ls <pkgpath>`) List all the exported symbols (functions, constants, types) from a given Go package by leveraging the `goscan.Scanner`. For example, `:pkg strings` would list `ToUpper`, `ToLower`, `Join`, etc.
*   **:reset**: Clear the current interpreter state (variables, functions) and start fresh, without having to restart the REPL application itself. This would involve creating a new `minigo.Interpreter` instance.

### 5.2. Pros and Cons

*   **Pros**:
    *   Adds powerful introspection and debugging capabilities, making the REPL a much more useful tool.
    *   Allows for environment control that is impossible from within the language itself.
*   **Cons**:
    *   Increases the complexity of the REPL's logic.
    *   Some features might require small modifications to the core `minigo` and `go-scan` libraries.

### 5.3. Required Core Library Changes

This approach requires the most significant changes to the underlying libraries, as it needs to expose more of the interpreter's internal state and enhance the `go-scan` library's capabilities.

*   **For `:ls` and `:cd`**:
    *   **`minigo.Interpreter` needs a package context**: It needs a new field, e.g., `currentPackageContext string`, to store the path set by `:cd`.
    *   **`minigo.Interpreter` needs a context management method**: A method like `SetPackageContext(path string) error` would be needed to validate and set the context.
    *   The `ListSymbols()` method proposed in Approach 2 would need to be enhanced to `ListSymbols(pkgPath ...string) []string`, allowing it to list symbols from either the `globalEnv` or a specified package.

*   **For package symbol listing (`:ls some/pkg`)**:
    *   **`go-scan` needs a way to list all package members**: The current `goscan.Scanner` is designed to find specific symbols on demand (`FindSymbolInPackage`). It does not have a public method to simply scan an entire package and return all of its members.
    *   A new method on `goscan.Scanner`, such as `ScanPackageFromFilePath(path string) (*goscan.Package, error)`, would be required. This method would proactively parse all files in a package and return a complete `Package` object containing lists of all its `Constants`, `Vars`, `Funcs`, and `Types`. This is a fundamental addition to `go-scan`'s capabilities.

### 5.4. Analysis of Dynamic Imports

A key feature for a REPL is the ability to import packages on the fly. This could be implemented in two main ways.

#### 5.4.1. Option A: As a Language Feature

In this model, the user would just type `import "fmt"` directly into the REPL.

*   **Implementation**: The `minigo` evaluator already has logic to handle `import` statements within `evalGenDecl`. However, this logic is tied to a `*object.FileScope`. To make this work, the REPL's `Interpreter` instance would need to own a single, persistent `FileScope` that represents the entire REPL session. Each line of input would be evaluated within this scope, allowing `import` statements to progressively add aliases to it.
*   **Pros**: This is the most natural and user-friendly option, as it uses standard Go syntax. It properly leverages the existing evaluator logic.
*   **Cons**: It requires careful management of the REPL's "session scope" object within the `Interpreter`.

#### 5.4.2. Option B: As a Meta-Command

In this model, the user would type `:import "fmt"`.

*   **Implementation**: The REPL loop would recognize the `:import` command and would not pass the line to the evaluator. Instead, it would parse the package path and call a new method on the interpreter, e.g., `ImportPackage(path string, alias string) error`. This method would then manually update the persistent "session scope" with the new package information.
*   **Pros**: It cleanly separates the REPL's functionality from the core language evaluator. The evaluator's code would not need to be touched.
*   **Cons**: It introduces non-standard syntax that the user must learn, making the REPL behave differently from a standard `.minigo` script.

#### 5.4.3. Recommendation & Required Changes

Both options require the same fundamental change: **the `Interpreter` must manage a persistent `*object.FileScope` for the REPL session**.

Given this shared requirement, **Option A (Language Feature) is recommended**. It provides a superior user experience at a comparable implementation cost to Option B.

The required change would be to augment the `Interpreter` to create and hold a `FileScope` for the REPL session, and to ensure the proposed `EvalString()` method uses this scope for all evaluations.

## 6. Conclusion & Recommendation

All three approaches have merit and are not mutually exclusive.

*   **Approach 1 (Simple REPL)** is the essential foundation. Without it, nothing else is possible.
*   **Approach 2 (Rich REPL)** is a significant user experience enhancement that makes the tool far more pleasant and productive to use.
*   **Approach 3 (Meta-Commands)** adds a layer of professional tooling that enables deep inspection and control, transforming the REPL from a toy into a serious development tool.

**Recommendation:**

The recommended path is a phased implementation:

1.  **Phase 1: Implement the Simple REPL (Approach 1).** This delivers immediate value and provides the core interactive functionality. This step would require creating a new `EvalString()` or similar method on the `Interpreter`.
2.  **Phase 2: Add essential Meta-Commands (Approach 3).** After the simple REPL is working, add `:help`, `:symbols`, and `:reset`. These provide high value for a relatively low implementation cost.
3.  **Phase 3: Transition to Bubble Tea (Approach 2).** Once the core logic is proven, replace the `bufio.Scanner` loop with a `bubbletea` front-end. This will immediately provide a better input experience and lay the groundwork for more advanced UI features.
4.  **Phase 4: Add advanced Meta-Commands and UI features.** With the `bubbletea` framework in place, incrementally add more complex features like `:cd`, `:pkg`, and autocompletion.
