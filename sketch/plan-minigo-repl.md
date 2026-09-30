> [!NOTE]
> This feature has been implemented.

# MiniGo REPL Implementation Plan

## 1. Overview

This document outlines the implementation plan for a basic Read-Eval-Print Loop (REPL) for the `minigo` language. The goal is to provide a simple, interactive command-line interface for executing `minigo` code snippets, inspecting results, and experimenting with the language features without needing to create a file for every script.

The REPL will support:
- A basic line-by-line evaluation loop.
- Persistent state for variables and functions within a session.
- The ability to import Go packages dynamically using the standard `import` keyword.
- A few essential meta-commands for controlling the REPL session (e.g., `:help`, `:reset`, `:exit`).

## 2. Core REPL Implementation

### 2.1. REPL Entrypoint

The main entrypoint in `examples/minigo/main.go` will be modified. It will check the number of command-line arguments:
- If an argument (a filename) is provided, it will execute the file as it currently does, preserving existing functionality.
- If no arguments are provided, it will start the REPL.

The REPL loop itself will be simple, using `bufio.Scanner` to read lines from standard input.

### 2.2. New `Interpreter` Method: `EvalLine`

The core of the REPL is the ability to evaluate a single line of code. The current `Interpreter.Eval` method is designed for whole files. A new method, `EvalLine`, will be added to `minigo/minigo.go`.

```go
// in minigo/minigo.go
func (i *Interpreter) EvalLine(ctx context.Context, line string) (object.Object, error)
```

This method will:
1.  Parse the input `line` into an AST. Since the parser expects a file, we'll use a placeholder filename like `"REPL"`.
2.  Evaluate each declaration/statement in the parsed line against the interpreter's persistent `globalEnv` and a dedicated `replFileScope`.
3.  Return the `object.Object` result of the last expression evaluated, so the REPL can print it.
4.  If an error occurs, it will be returned and printed to the user without crashing the REPL.

## 3. State Management for the REPL

To ensure that variable declarations (`x := 10`) and `import` statements persist across multiple lines of input, the `Interpreter` will manage a single, dedicated `*object.FileScope` for the entire REPL session.

1.  A new field, `replFileScope *object.FileScope`, will be added to the `Interpreter` struct.
2.  When the REPL starts, this `replFileScope` will be initialized once.
3.  The new `EvalLine` method will use this single scope for all evaluations. This allows the scope's import map (`Imports`) to be built up cumulatively as the user enters `import` statements.

## 4. Meta-Commands and Features

Meta-commands will be prefixed with a colon (`:`) and handled by the REPL loop in `main.go` before the line is sent to the interpreter.

### 4.1. Import Handling (Language Feature)

As recommended in the analysis, `import` will be a standard language feature, not a meta-command.
- **Syntax:** `import "fmt"` or `import f "fmt"`
- **Implementation:** The `evaluator` already handles `import` statements. By using a persistent `replFileScope` (as described in section 3), the results of these imports will be correctly stored and available for subsequent lines of code.

### 4.2. Implemented Meta-Commands

The following meta-commands will be implemented:

- **`:help`**:
    - **Action:** Prints a brief help message listing available commands and features.
    - **Implementation:** A simple `fmt.Println` in the `main.go` REPL loop.

- **`:reset`**:
    - **Action:** Resets the interpreter's state, clearing all variables, functions, and imports from the current session.
    - **Implementation:** The REPL loop will discard the current `*minigo.Interpreter` instance and create a new one by calling `minigo.NewInterpreter()`. This is a clean and effective way to reset the entire environment.

- **`:exit`**:
    - **Action:** Exits the REPL and terminates the program.
    - **Implementation:** A `break` statement in the REPL's `for` loop.

## 5. Implementation Specifications

### 5.1. Current Vision

The vision is to create a lightweight, intuitive REPL that behaves similarly to other interactive language shells (like Python or Node.js). A user can open it, type `x := 10`, then `x * 2`, and see the result `20`. They can import a package, use it, and then reset the session to start fresh. The focus is on correctness and utility over advanced features like syntax highlighting or auto-completion for this initial version.

### 5.2. Concerns and Challenges

1.  **Error Handling:** The `EvalLine` method must be robust. It needs to catch panics and errors from the evaluator and convert them into a standard `error` return value. The REPL loop will then print this error and continue, rather than crashing.
2.  **State Management Correctness:** The most critical part of the implementation is ensuring the `replFileScope` is managed correctly. If it's re-created on every line, `import` and variable state will be lost. It must be created once and passed into every `EvalLine` call for the session.
3.  **Multi-line Statements:** The initial implementation using `bufio.Scanner` will not handle multi-line input gracefully (e.g., pasting a function definition). This is a known limitation. We will accept single-line inputs only for now, with a note that more complex definitions should be loaded from files.

## 6. Incremental Task List

- [ ] **Task 1: Modify `main.go` to conditionally start the REPL.**
    - If `len(os.Args) == 1`, start the REPL.
    - Otherwise, run the file specified in `os.Args[1]`.
- [ ] **Task 2: Implement the basic REPL loop in `main.go`.**
    - Use `bufio.NewScanner` to read from `os.Stdin`.
    - Print a prompt `>> `.
    - Handle the `.exit` command.
- [ ] **Task 3: Add the `replFileScope` field to the `Interpreter` struct.**
    - Add `replFileScope *object.FileScope` to `minigo.Interpreter` in `minigo/minigo.go`.
    - Initialize it in a new function, e.g., `NewInterpreterForREPL`.
- [ ] **Task 4: Implement the `EvalLine` method in `minigo.go`.**
    - Create the `EvalLine(ctx, line)` method on the `Interpreter`.
    - It should parse the line, evaluate it using the `replFileScope`, and return the result or an error.
- [ ] **Task 5: Integrate `EvalLine` into the REPL loop.**
    - Call `interp.EvalLine()` for each line of input.
    - Print the result's `Inspect()` output or the error message.
- [ ] **Task 6: Implement the `.help` and `.reset` meta-commands.**
    - Add `if/switch` block in the `main.go` REPL loop to handle these commands.
- [ ] **Task 7: Verify `import` statement functionality.**
    - Write a manual test case by running the REPL and typing `import "strings"`, followed by `strings.ToUpper("hello")`.
- [ ] **Task 8: Add tests for the new REPL functionality.**
    - While a full integration test of the REPL is complex, unit tests for `EvalLine` should be added to `minigo_test.go` to cover success, error, and state-retention cases.
- [ ] **Task 9: Run `make format` and `make test` to ensure all checks pass.**
