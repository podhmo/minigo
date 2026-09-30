# Deep Dive Analysis of the `slices.Sort` Timeout

This document provides a detailed, evidence-based analysis of the performance timeout encountered when the `minigo` interpreter executes `slices.Sort` on a slice loaded from source.

## The Problem

When `slices.Sort` is called on a non-empty slice within the interpreter, the execution takes an extremely long time (>400s), effectively a timeout. Initial suspicions about an infinite loop in the interface checking logic were proven incorrect through a series of diagnostic tests.

## Investigation and Key Findings

A multi-step investigation was performed to isolate the root cause.

### Finding 1: The Inconclusive First Stack Trace

An initial stack trace, captured by terminating the test after 30 seconds, pointed to `runtime.newobject` being called from `inferTypeOf`. This led to a hypothesis that the interpreter was allocating millions of temporary `object.Type` objects inside the sorting algorithm's loops. A fix was implemented to use singleton objects for primitive types.

**Result:** The tests *still* timed out, proving that while the memory churn in `inferTypeOf` was a real inefficiency, it was not the primary cause of the timeout.

### Finding 2: The Definitive Second Stack Trace

A second stack trace, captured after the first fix failed, provided the "smoking gun."

```
goroutine 6 gp=0xc000003a40 m=nil [runnable]:
...
runtime.makemap_small()
	/usr/local/go/src/runtime/map_swiss.go:53 +0x1a
github.com/podhmo/go-scan/minigo/object.NewEnvironment(...)
	/app/minigo/object/object.go:993
github.com/podhmo/go-scan/minigo/object.NewEnclosedEnvironment(...)
	/app/minigo/object/object.go:999
github.com/podhmo/go-scan/minigo/evaluator.(*Evaluator).evalBlockStatement(0xc0001142c0, 0xc0003610e0, 0xc0003b1d80, 0xc000385f50)
	/app/minigo/evaluator/evaluator.go:1346 +0x4f
...
github.com/podhmo/go-scan/minigo/evaluator.(*Evaluator).evalForStmt(...)
...
```

**Analysis:** This trace clearly shows the interpreter allocating a new map (`runtime.makemap_small`) to create a new environment (`object.NewEnclosedEnvironment`). This was being called from `evalBlockStatement`, which in turn was being called from `evalForStmt`. This revealed that the interpreter was creating a completely new environment object for the body of a `for` loop on **every single iteration**.

## Root Cause Analysis: Massive Environment Allocation in Loops

The definitive root cause of the timeout was a catastrophic performance bug in the `evalForStmt` function.

The original implementation was `bodyResult := e.Eval(fs.Body, loopEnv, fscope)`. Since `fs.Body` is an `*ast.BlockStmt`, this dispatched to `evalBlockStatement`, which begins by creating a new enclosed environment.

When interpreting the `slices.Sort` algorithm, which contains nested `for` loops, this implementation detail meant that for every iteration of an inner loop, a new environment (and its associated map) was allocated. This created millions of unnecessary allocations, leading to extreme garbage collection pressure and causing the interpreter to grind to a halt.

### Secondary Bug: Incomplete Built-in Type List

A secondary, unrelated bug was also discovered during the investigation. The `evalIdent` function, which recognizes built-in type names like `int`, was missing many numeric types (e.g., `int8`, `uintptr`). This caused a crash when the interpreter tried to parse the `cmp.Ordered` interface. This was fixed by adding the missing types to the list.

## The Final Solution

The solution was to refactor `evalForStmt` to be more efficient. Instead of evaluating the entire block statement node on each iteration, the new implementation iterates through the statements *within* the block (`fs.Body.List`) directly. This ensures that the single `loopEnv` created for the `for` statement is reused for every iteration, eliminating the massive allocation overhead.

This change fixed the timeout issue, and all `slices.Sort` tests now pass in under 0.1 seconds.
