# Profiling

minigo has two kinds of profiles:

| flag | env default | what it profiles |
|---|---|---|
| `--scriptprofile f` | `$MINIGO_SCRIPTPROFILE` | the interpreted program: script functions and lines |
| `--cpuprofile f` | `$MINIGO_CPUPROFILE` | the interpreter itself (Go CPU profile of minigo) |
| `--memprofile f` | `$MINIGO_MEMPROFILE` | the interpreter itself (Go allocation profile, written at exit) |

All three write pprof profiles, so you read them with `go tool pprof`. They are
written even when the script traps or calls `os.Exit(N)`.

Use the env defaults to profile a wrapper script that runs `minigo` without
editing its command line:

```shell
MINIGO_SCRIPTPROFILE=/tmp/script.pprof ./task.sh minigo
```

## Script profile

```shell
minigo run --scriptprofile script.pprof ./tool -- args...
go tool pprof -top script.pprof          # by function
go tool pprof -top -lines script.pprof   # by line
go tool pprof -http=: script.pprof       # flame graph
```

Functions are named the way Go's pprof names them: `main.fib`,
`example.com/p.(*T).M`, `main.run.func1`.

Embedders use the same profiler through the API:

```go
p := minigo.NewScriptProfiler(0) // 0 = sample every 10ms
e := minigo.NewEngine(dir, minigo.WithScriptProfiler(p))
_, err := e.Run(ctx, ref, "main")
p.Stop()
_, err = p.WriteTo(f)
```

### What the samples mean

- **The samples measure wall time, not CPU time.** A clock goroutine advances a
  tick every period. Each script goroutine checks the tick at its safepoints
  and records its script call stack, weighted by the number of ticks since its
  last check.
- **The safepoints** are before a frame is pushed, before it is popped, at loop
  back-edges, and after a call returns. Because every push and pop is a
  safepoint, the code that runs between two checks always has the same
  innermost frame. The attribution is therefore exact up to the tick period.
- **Call overhead is charged to the calling line.** The caller's share of a call
  (evaluating arguments and setting up the frame) shows on the line of the call.
  The callee is charged from its entry up to its return.
- **A host call is charged to the line that made it.** A host call (for example
  `strings.Builder.WriteString` or `time.Sleep`) has no script frame of its own.
- **Blocked time is excluded.** Time a goroutine spends blocked on a channel
  operation, a `select`, or a blocking host-call join is not counted. Every
  channel op parks, even one that completes at once, so an op only counts as
  blocked when it spans more than one tick. A real wait therefore loses at
  most one tick of run time, and a quick op (a receive on a closed channel, a
  `select` with a ready case) is counted as run time. Host sleeps are counted,
  because they are host calls.
- **Deferred calls are charged to the function that defers them.** That
  includes a deferred host call such as `defer time.Sleep(d)`, which has no
  script frame of its own.
- **When off, the profiler costs almost nothing.** With no profiler set, each
  safepoint costs one nil check, which is not measurable.
- **When on, it costs little.** On a 90s workload (gosimilarity), the run time
  stays within noise.

### Interpreter profiles

`--cpuprofile` and `--memprofile` show the interpreter's Go frames (`VM.loop`,
`VM.call`, GC). They answer "what part of minigo is slow". The script profile
answers "what part of the script is slow". To work on interpreter performance
(see `TODO.md`), start with the script profile to find the hot script code, then
use the CPU and allocation profiles on the same run to see what the interpreter
spends on it.

### Known gaps

- There are no exact call counts: a time profile cannot say that `f` ran 21M
  times.
- Script allocations are not attributed to script stacks.
- A closure inside a method prints as `T.m.func1`, where Go prints
  `(*T).m.func1`.

These gaps are tracked in `TODO.md`.
