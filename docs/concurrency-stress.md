# Shared-engine concurrency stress

This is the first implementation of direction C from [#1062](https://github.com/podhmo/minigo/issues/1062).
It exercises selected interpreter-owned shared metadata and host callback entry
paths using a shared Engine and independent root Calls. It does not parallelize
the differential corpus, prove race freedom, or implement a lifecycle model.

## Run

```sh
git rev-parse HEAD
make concurrency-stress
```

The default matrix uses `GOMAXPROCS=1,2,4`, 12 fresh engines per case, 8 workers
per engine, and 10 calls per worker: 960 calls per case per CPU setting. The
Go test process has a two-minute deadline for the entire matrix. There are no
random inputs; scheduling is nondeterministic. Output records the toolchain,
platform, current CPU setting, case names and configured bounds. Record the
commit printed above alongside the output.

Override the bounds or select cases explicitly:

```sh
make concurrency-stress CONCURRENCY_STRESS_CPU=2 CONCURRENCY_STRESS_TIMEOUT=3m \
  CONCURRENCY_STRESS_ARGS='-concurrency-stress-rounds=20 -concurrency-stress-workers=8 -concurrency-stress-calls=30 -concurrency-stress-cases=ArrayTypes,CallbackRelease'
```

Unknown/duplicate case names and nonpositive bounds fail the test. `-count=1`
prevents Go's test cache from replacing a stress run. The process deadline is
intentional: a context deadline or an abandoned worker goroutine cannot reliably
terminate arbitrary host operations. Workers are joined, and failures propagate
to the test's exit status. Race reports are never treated as expected success.

## Fixtures and ownership

| Case | Coverage | Expected result |
| --- | --- | --- |
| `ArrayTypes` | Named/nonliteral and nested array lengths, ellipsis, reflection, interface dispatch | 16 |
| `SyncReentry` | Invocation-local `sync.Once` and sort callbacks | 2 |
| `FuncMap` | Template callbacks with invocation-local template/output | `"6"` |
| `CallbackRelease` | #1061's `testdata/procrace.CancelThenReturn`: `context.AfterFunc` cancellation immediately before root process release | `"ok"`, plus no race report |

Each round loads a package once, then releases host workers together through a
start barrier. Loading is not warmed with a sequential Call first: the first
concurrent invocations exercise lazy compilation/materialization, and subsequent
calls reuse warm caches. New engines in later rounds restore first-use windows.
The barrier permits overlap; it does not force a particular VM interleaving.

Fixtures have no mutable package globals, filesystem/process effects, shared
output buffer or mutable callback capture. `Package` plus `Call` avoids `Run`'s
main-package metadata path. Engine output is discarded; the FuncMap buffer is
owned by its invocation. A pure value oracle checks every result using `go-cmp`.
The release callback intentionally does not join, since joining would remove
the termination window being investigated. Its body is empty and any remaining
callback execution is bounded by the test process's lifetime.

Do not add arbitrary `testdata/difffuzz` cases to this allowlist. Cases writing
package globals need their own synchronization or independent engines. Process
I/O redirection, intentional hangs, leaked host calls and fatal-process cases
need subprocess isolation. Such cases are not supported by this runner.

## Relationship to #1061 and ordinary CI

The implementation builds on main after
[#1061](https://github.com/podhmo/minigo/pull/1061) merged. That PR owns the
`atomic.Pointer[proc]` fix, `TestHostCallbackRacesProcRelease`, and
`testdata/procrace`. This change neither copies that regression test nor
modifies the VM. `CallbackRelease` directly reuses `CancelThenReturn` from
that fixture through the general configurable cold/warm runner; there is no
second cancellation fixture and no claim of a second bug.

With the pre-#1061 VM implementation, the callback case should report the known
pointer race between `ReleaseProc` and callback entry. Run the same harness
against both implementations to check its negative and positive controls; the
PR records the actual revisions tested. A race on the fixed implementation
requires investigation, rather than an expected-failure exemption.

`TestConcurrencyStressSmoke` runs 2 engines x 2 workers x 2 calls for all four
cases in ordinary `make test` and `make test-race`. The larger
`TestConcurrencyStress` is skipped unless `-concurrency-stress` is supplied.
The smoke suite checks the runner across the allowlisted paths; #1061's
dedicated regression keeps its larger fixed callback/release workload. No
race failure is suppressed and no scheduled workflow is introduced. The opt-in
suite can be used before releases or as the basis for a measured scheduled job.

The current matrix does not assert callback admission/lifetime semantics,
panic recovery policy, deadlock detector correctness, or absence of goroutine
leaks. Those require separate controlled scenarios and explicit contracts as
described in #1062. A passing bounded stress run only covers executed schedules.
