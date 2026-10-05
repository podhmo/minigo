# plan-task-runner.md — a mage/go-task style task runner on minigo

Companion to `docs/sketch/plan-minigo-vm.md`. This file plans the runner itself
(`examples/task-run`); the minigo features the runner forced are recorded
in plan-minigo-vm.md's round-13 notes and tracked in TODO.md.

## 1. What it is

`task-run` executes a **Taskfile** — a plain Go source file, usually with a
`//go:build task` tag so `go build ./...` skips it — through the minigo
interpreter. Any exported function of the shape

```go
func Name()
func Name() error
func Name(a string, b string) (error)   // params must be `string`
```

is a task. Its doc comment's first line becomes the `-l` description, like
mage's `mg -l`. This is deliberately the mage model (tasks are functions,
not YAML targets): the point of the exercise is whether minigo can drive a
real tool-shaped consumer, not to clone go-task.

## 2. CLI

```
task-run [-f Taskfile.go] -l
task-run [-f Taskfile.go] Name[:arg1,arg2] ...
task-run [-f Taskfile.go] -n Name ...      # dry run (§6)
```

- default taskfile: `./Taskfile.go`
- no task name → `Default`
- colon-args keep multi-task invocation unambiguous (`task-run Deploy:prod Lint`)

Distribution: `go install` produces a standalone `task-run` binary in
`$(go env GOPATH)/bin` — the interpreter is compiled in once, so day-to-day
invocation is `task-run -f Taskfile.go <task>` with zero rebuild; `go run`
remains the dependency-free dev path.

## 3. The `task` package (script-side API)

`examples/task-run/task/task.go` is a **stub package**: every body is
`panic("minigo intrinsic")`, and the engine binds the import path
(`task` and the full module path alike) to intrinsics — the same trick
`minigo.dev/host` uses. That keeps taskfiles editable under real Go
tooling (gopls resolves the package, signatures and docs are real).

| API | mage/go-task analog | semantics |
|-----|--------------------|-----------|
| `task.Deps(fns...)` | `mg.Deps` | each dep runs once per invocation, in parallel on spawned goroutines; cycle → error |
| `task.SerialDeps` | `mg.SerialDeps` | serial ordering on the caller's goroutine; dedup shared with `Deps` |
| `task.F(fn, args...)` | `mg.F` | wrap fn+args into a callable dep, dedup key (fn, args) |
| `task.Sh(cmd)` | `sh.Run` / `cmd:` | `sh -c`, stdio on the runner's streams |
| `task.Run(name, args...)` | `sh.Run` | direct exec, streaming output |
| `task.RunIn(dir, name, args...)` | `sh.RunWith` | Run with a working directory |
| `task.Output(name, args...)` | `sh.Output` | returns trimmed stdout |
| `task.Target(target, deps...)` | `mg.Target` | mtime up-to-date check: target exists and is not older than any dep |
| `task.Log(args...)` | — | task log line on stderr |
| `task.Env(name)` | `os.Getenv` | env read |

Deps arrive as ordinary **function values** — `task.Deps(Build)` passes the
`*runtime.Function` itself; no quoting or special forms needed. That is a
verified consequence of minigo's design: referencing a symbol materializes
it lazily, and calling it is the script's decision.

## 4. Engine choices

- **Unrestricted host surface** (no `AllowedRoots`): taskfiles are trusted
  build scripts — they need `os/exec`, env reads, and free filesystem
  access. Restricted mode exists for untrusted DSLs and keeps its per-call
  path checks (round-13); the runner doesn't opt in.
- **Virtual cwd = the Taskfile's directory** (`WithWorkingDir(dir)`):
  scripts' relative paths resolve against it, `exec.Command` defaults
  `cmd.Dir` to it, and `os.Chdir` inside a task never moves the host
  process. go-task does the same (Taskfile-relative `dir:`); mage uses the
  invocation cwd instead — a documented divergence.
- `WithOutput(stdout)` so script `fmt.Println` reaches the terminal;
  `task.Log` goes to stderr to keep `task.Output`-style capture clean.

## 5. What minigo had to grow (all in round-13, plan-minigo-vm.md §32)

- `os` file APIs: `Stat`/`Lstat`/`ReadFile`/`WriteFile`/`Mkdir`/`MkdirAll`/
  `Remove`/`RemoveAll`/`Rename`/`Truncate`/`ReadDir`/`Open`/`Create`/
  `OpenFile`/`MkdirTemp`/`CreateTemp`, `IsNotExist`-family + sentinels,
  `O_*`/`Mode*`/seek consts, `Getwd`/`Chdir` on the virtual cwd.
- `path/filepath`: `Join`/`Base`/`Dir`/`Ext`/`Clean`/`IsAbs`/`Match`/
  `Abs`/`Rel`/`Glob`/`EvalSymlinks`/`WalkDir`/`SplitList`/`ToSlash`/
  `FromSlash`/`VolumeName`/`Split`… and `SkipDir`/`SkipAll` sentinels.
- `os/exec` (unrestricted only): `Command`/`LookPath` + `Err*` sentinels —
  `*exec.Cmd` is a boxed `*runtime.GoValue`, so `cmd.Dir`/`cmd.Stdout`
  needed **host-struct field get/set**, which did not exist.
- `*runtime.GoValue` field access + typed method-call marshalling
  (`toReflectValue`): `cmd.Dir = "sub"`, `cmd.Stdout = os.Stdout`,
  `f.Write(bytes)`.
- `[]byte`/`[]string`/`time.Duration`/integer-width unmarshalling in
  `goValueOf`/`scriptVal`, so `cmd.Output()`'s `[]byte` feeds `string(b)`.

## 6. Dry run (`-n`): swap the standard library, not the script

The aim: **because the Taskfile is interpreted, the host owns every effect
boundary**, so `make -n`-style "show me what would run" falls out of
rebinding a handful of standard-library symbols — the script itself is
not rewritten, annotated, or written against a special API.

Compare the alternatives:

- **make -n** only knows recipe lines as text. It prints them, but
  `$(shell ...)` still runs, and anything a recipe does through an
  interpreter it spawns is opaque.
- **mage** compiles the magefile into a real binary: `os.WriteFile` or
  `exec.Command(...).Run()` in a target is a direct syscall path. A dry
  run is only possible if every target routes through `sh.Run`-style
  helpers that check a flag — plain Go calls cannot be intercepted.
- **task-run** resolves `os`, `os/exec` and `task` to host bindings
  (`Engine.Bind`), and the bindings are looked up through the package's
  globals at call time. `Runner.SetDryRun` fetches the bound packages via
  `Engine.Package(ctx, "os")` and replaces individual entries:

| binding | dry-run replacement |
|---------|---------------------|
| `task.Sh` / `Run` / `RunIn` / `Output` | print the command line; `Output` returns `""`, nil |
| `os.WriteFile` / `Remove` / `RemoveAll` / `Mkdir` / `MkdirAll` / `Rename` / `Truncate` | print `# os.Name args...`, return nil |
| `exec.Command` | returns `dryCmd`, a stand-in for `*exec.Cmd` |

`dryCmd` works because host values are reached by reflection (§5): field
set (`cmd.Dir = "sub"`, `cmd.Stdout = os.Stdout`) and method calls
(`Run`/`Start`/`Wait`/`Output`/`CombinedOutput`) dispatch on whatever
concrete type the GoValue boxes. Printing happens at `Run` time, not at
`Command` time, so a `Dir` set after construction shows up as `(in dir)`
and a command built but never run prints nothing. It even passes through
a script function typed `func(c *exec.Cmd)` — the interpreter does not
check the boxed host type against the declared one.

Everything else keeps running for real: control flow, `fmt.Println`,
`task.Log`, and **reads** (`task.Target`, `os.Stat`, `os.ReadFile`,
`filepath.Glob`). The dry run therefore follows the branch the tasks
would take against the current filesystem state, which is what makes it
useful as "what would `Dist` do right now?".

Known gaps, by design of the replacement rule ("only error-returning
calls are safe to fake"):

- Values a fake cannot know are empty: `task.Output` returns `""`,
  `cmd.Output()` returns no bytes. Code branching on command output may
  take a different path than the real run (make -n avoids this for
  `$(shell)` by running it; here safety wins).
- Handle-returning calls (`os.Create`, `os.OpenFile`, `os.CreateTemp`,
  `os.MkdirTemp`) and env mutation (`os.Setenv`) are not intercepted.
- Effects reached through other bound packages (e.g. a future `net/http`
  binding) are not covered until they get their own replacement.

The same lever generalizes beyond `-n` — sketches, not plans:

- **trace mode**: wrap instead of replace (print, then delegate to the
  original binding) for an `-x`-style execution log.
- **sandboxed run**: point the write-side `os` bindings at a scratch dir
  or an in-memory FS and diff the result.
- **recorded outputs**: feed `task.Output` / `cmd.Output()` from a
  fixture file so dry runs and tests can follow output-dependent branches.

## 7. Deliberately not done (follow-ups)

- **`[]byte` spellings.** `[]byte("x")` conversion is still unimplemented
  in the interpreter — task scripts use `os.WriteFile(p, "x", 0644)` (the
  intrinsic accepts strings) or `f.Write("x")` (string→[]byte converts via
  reflect).
- **Task arg typing.** Params are `string` only; `task-run Build:dbg` style
  flag/env binding (mage uses env vars) could add `task.Args`/`task.Flag`.
- **Discovery.** `-f` only; no upward Taskfile search like `task -t`.
- **`task.Target` is mtime-only** — no content-hash staleness, no mkdir of
  the target's parent.
- **No namespaces / taskfile imports.** Composing across files means
  calling tasks that live in one file; multi-file task sets want a
  per-file package model (LoadFile already exists; `task.Namespace` +
  `ns.Task` dispatch is the sketch).
- **No `task.Context`** — no cancellation wiring; `exec.CommandContext`
  would want a host `context.Context` bound as a GoValue.
- **`minigo vet` integration**: the task stub package would report clean
  automatically (all members are bound), but a `--special`-style check that
  task names exist could catch typos statically.

## 8. Verification

- `runner_test.go` — `TestTasks` (listing + docs + params), `TestRunTask`
  (Default dep-chain writing `app.out`), `TestDepsDedup`,
  `TestDepsCycle`, `TestDepsWithArgs` (`task.F`), `TestShAndTarget`
  (cwd anchoring + mtime check), `TestRunOutput`, `TestTaskErrors`
  (error propagation, non-task rejection), `TestRunMainList`,
  `TestDryRun` / `TestDryRunExec` (`-n` prints `task.*`, `os` mutators and
  `os/exec` commands; the Taskfile's directory is left untouched).
- `minigo` intrinsic coverage: `TestFSIntrinsics`, `TestVirtualCwd`,
  `TestExecIntrinsics`, `TestFSRestricted` in `minigo_test.go` over
  `testdata/fsops`.

## (end)
