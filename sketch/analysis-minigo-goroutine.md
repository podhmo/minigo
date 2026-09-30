# Analysis of Concurrency in `minigo` and `go-scan`

## 1. Introduction

This document analyzes the feasibility and potential implementation plan for adding concurrent processing capabilities to both the `minigo` interpreter and the underlying `go-scan` library.

For `minigo`, the goal is to evaluate adding `goroutine` and `channel` support. For `go-scan`, the goal is to evaluate parallelizing the scanning process to improve performance. The analysis considers the required architectural changes, implementation complexity, and benefits in performance and safety.

## 2. Concurrency in the `minigo` Interpreter

### 2.1. Current Architecture

`minigo` is an AST-walking interpreter. Its core logic resides in the `minigo/evaluator/evaluator.go` file. The key components of its current architecture are:

*   **`Evaluator` Struct**: This is the main object that drives the interpretation process. It holds the state for a *single, sequential* execution, including:
    *   `callStack`: A stack of `CallFrame` objects that tracks the current function call hierarchy.
    *   `packages`: A cache of loaded packages.
    *   `registry`: A registry for symbols (functions, variables) injected from the host Go application.

*   **`Eval()` Function**: The `Eval(node, env, fscope)` function is the heart of the interpreter. It's a large, recursive function that traverses the Abstract Syntax Tree (AST).

This architecture is inherently single-threaded and not designed for concurrent access.

### 2.2. Challenges of Introducing Concurrency

Directly using Go's goroutines to execute `minigo` code concurrently is not possible without major changes. Any attempt would lead to severe race conditions, primarily due to:

*   **Shared Mutable State**: The `Evaluator` struct is stateful and not thread-safe. The `callStack` is the most critical point of contention.
*   **Lack of an Interpreter-Level Scheduler**: `minigo` would need its own scheduler to manage `minigo` "goroutines" and handle blocking operations (like channel reads) without blocking the underlying OS thread.
*   **State Isolation**: Each concurrent task in `minigo` would need its own independent execution stack.

### 2.3. Conclusion for `minigo`

Adding goroutine support to `minigo` is a significant architectural project. It requires implementing a scheduler, redesigning state management for thread safety, and introducing new language primitives (`go`, `chan`, `select`). This is a substantial effort.

**Recommendation:** It is recommended **not to proceed** with adding goroutine support to `minigo` at this time, as the complexity is very high.

## 3. Concurrency in the `go-scan` Library

In contrast to `minigo`, the `go-scan` library is much better positioned to benefit from concurrency to improve performance.

### 3.1. Current Architecture

*   **`goscan.Scanner`**: The main entry point, which holds state like caches (`packageCache`, `symbolCache`) and a set of `visitedFiles`.
*   **`scanner.Scanner`**: The core worker that performs the actual file parsing (`parser.ParseFile`) and AST traversal.
*   **`sync.RWMutex`**: Crucially, the `goscan.Scanner` struct already contains a `sync.RWMutex`, which is used to protect access to its various caches.

### 3.2. Analysis of Concurrency Potential

The most CPU-intensive part of the scanning process is parsing `.go` files and walking their ASTs. This work is highly parallelizable. The `scanner.scanGoFiles` method, which currently processes files sequentially, is the primary candidate for parallelization.

**Proposed Concurrent Implementation:**

The `scanner.scanGoFiles` method can be refactored to process files concurrently:

1.  For a given package, iterate through its list of `.go` files.
2.  For each file, launch a new Go goroutine to perform the parsing and initial AST processing.
3.  Each goroutine would produce a "partial" `PackageInfo` struct containing only the symbols from that single file.
4.  Use a `sync.WaitGroup` to wait for all file-parsing goroutines to complete.
5.  Collect the partial results from each goroutine (e.g., via a channel).
6.  After all goroutines are done, merge the partial results into a single, final `PackageInfo` for the package. This merge step must be done sequentially.

### 3.3. Required Changes for Safe Concurrency

While the caches are already protected by a mutex, a few additional changes are needed:

1.  **Synchronize `visitedFiles`**: The `goscan.Scanner.visitedFiles` map is accessed and modified without lock protection. It must be protected by the existing `s.mu` mutex to prevent race conditions where two goroutines might try to parse the same file.
2.  **Refactor `scanGoFiles`**: The method in `scanner/scanner.go` needs to be rewritten to manage the pool of goroutines, collect results, and merge them.
3.  **Error Handling**: A strategy for handling errors from concurrent operations is needed. Using a library like `golang.org/x/sync/errgroup` would be appropriate to manage errors and cancel outstanding work if one goroutine fails.

### 3.4. Performance and Safety

*   **Performance**: For packages with many files, or for scans of entire projects, the performance improvement from parallel file processing should be significant. The overhead of managing goroutines would be negligible compared to the gains from parallelizing I/O and CPU-intensive parsing.
*   **Safety**: With the proposed changes (locking `visitedFiles` and careful merging of results), the concurrent implementation can be made safe. The existing use of a mutex for caches provides a strong foundation for this.

## 4. Overall Conclusion & Recommendation

### `minigo`
Adding goroutine support is a major, complex project requiring a fundamental redesign. **The recommendation remains not to proceed.**

### `go-scan`
Adding concurrent processing capabilities is highly feasible and promises significant performance benefits. The existing architecture is already partially prepared for this.

**Recommendation:** It is **recommended to proceed** with implementing concurrent scanning in `go-scan`. The effort is manageable and the performance payoff is high. Key steps would be to protect all shared state with the mutex and refactor the file processing loop to be parallel.
