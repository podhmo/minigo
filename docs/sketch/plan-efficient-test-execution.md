# plan-efficient-test-execution.md — faster minigo test execution paths

## Goal

PR #1447 adds `minigo test` and the in-process `internal/testrun`
runner. This plan checks where the repository's current test execution
still pays for process-per-test or engine-per-case isolation, then moves
only the workloads that keep their verdict semantics onto the cheaper
mechanism.

## Current execution forms found

- Root and examples tests are ordinary compiled `go test` runs. The
  dominant root cost is the 887-case difffuzz corpus, executed twice:
  once in normal mode and once under `WithStrictLazyCompile`.
- `tools/difffuzz` generates differential probes; its corpus is pinned
  back into root `go test` as `testdata/difffuzz`.
- `tools/tmpltests` copies upstream stdlib tests into a scratch GOROOT,
  renames `*_test.go`, generates a driver, and starts one minigo process
  per test. This isolates package state but repeats process startup,
  resolver setup, package indexing, and package initialization.
- Fuzz/usecase harnesses outside `make test` are differential or corpus
  runners, not candidates for `testing.T` execution.

## Measurements on this checkout

- `go test ./... -count=1`: 29.4s wall; root package 16.4s,
  `tools/difffuzz` 11.9s.
- `TestDiffRegressions` + `TestStrictLazyConformance`: 7.0s wall.
  Both suites run the same 887 independent cases serially. Deliberate
  deadlock-detection cases account for much of the idle wall time.
- `tmpltests` text/template with a prebuilt binary: 3.68s wall for 46
  process-per-test runs. `text/template/parse`: 0.96s for 17 runs.
- Running the same parse suite through PR #1447's in-process runner:
  0.24s wall, but the shared package state changes upstream-test
  semantics. Benchmarks merely present in the files are also reported
  as unsupported even though `go test` does not run them without
  `-bench`.

## Design

### 1. Add explicit isolation to `testrun`

Add `Options.Isolate` and `minigo test --isolate`. The default remains
go-test-like package state: package init once, tests sequentially share
that package. Isolated mode forks `Engine.NewSession()` per top-level
test and reloads the same package in that session. Parse/index caches
stay shared through the session fork, while globals, init results, and
test mutations do not cross tests.

This is the in-process equivalent of tmpltests' process-per-test rule.
It is not the default because it is a different contract from `go test`.

### 2. Let the runner use a scratch GOROOT

`tmpltests` needs the source packages and shims from its scratch GOROOT.
The resolver currently asks `go env GOROOT` once per process, so an
in-process library user cannot select that root. Add an explicit
resolver/locator option for GOROOT and thread it through
`testrun.Options`. The process-wide memoized `go env` lookup remains the
default.

### 3. Treat benchmarks as not-run, not as suite failures

`go test` compiles but does not execute `BenchmarkXxx` unless `-bench`
is supplied. Since `minigo test` has no `-bench` contract in v1, the
presence of benchmark declarations must not fail an otherwise runnable
test suite. They can remain undiscovered with a verbose note later;
they must not block upstream suites whose files mix tests and benchmarks.

Fuzz tests, examples with `// Output:`, external test packages, and
TestMain keep the existing loud-unsupported behavior because they do
affect what `go test` executes.

### 4. Migrate tmpltests' internal-test suites

Use `testrun` as a library for the non-example suites. Copy test files
under their real `_test.go` names, stop generating the driver for those
suites, and call `testrun.Run` with:

- the scratch GOROOT,
- `--src` package modes,
- `Isolate: true`,
- the existing `-run` selection.

Example suites stay on the existing process/EXOUT implementation until
`testrun` grows external test package and Example support.

### 5. Parallelize independent difffuzz corpus subtests

The two root corpus suites execute independent fixture directories.
Cases without `want.stderr` can use `t.Parallel`; the five cases that
temporarily replace process `os.Stderr` stay serial, because Go's test
scheduler runs non-parallel subtests before releasing paused parallel
ones. This changes scheduling, not verdict semantics.

Engine session reuse is not the first optimization here: construction is
only about 0.1ms after locator memoization, while fixture loading and
deadlock waits dominate. Measure again after parallelism before adding
complexity.

## Rejected options

- Running every tmpltests test in one shared package session: fastest,
  but upstream tests mutate package-level state and the text/template
  suite already shows different verdicts under that model.
- Implementing benchmark execution now: useful, but orthogonal to a
  correctness-suite migration and much larger than declaring benchmarks
  not-run in the no-`-bench` v1 contract.
- Moving examples/casefuzz/difffuzz generators onto `testing.T`: those
  harnesses compare process output or generate programs; they are not
  ordinary package unit tests.

## Acceptance

- `minigo test --isolate` keeps independent package state per test.
- tmpltests parse and text/template internal suites run in-process and
  preserve their existing per-test verdicts.
- Benchmark-containing upstream files do not fail merely because
  benchmarks exist.
- Root difffuzz corpus wall time improves without changing any fixture
  oracle.
- `make format`, `make lint`, and `make test` pass.
