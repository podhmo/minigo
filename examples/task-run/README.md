# task-run — a mage-style task runner on minigo

A task runner (in the spirit of [mage](https://magefile.org) /
[go-task/task](https://taskfile.dev)) where the Taskfile is a Go file
interpreted by [`minigo`](../../). See
[docs/sketch/plan-task-runner.md](../../docs/sketch/plan-task-runner.md) for the design.

## Usage

```sh
# one-off (compiles via the build cache each call)
go run ./ -f testdata/Taskfile.go -l      # list tasks (name + doc comment)
go run ./ -f testdata/Taskfile.go Default # run a task
go run ./ -f testdata/Taskfile.go Greet:world Clean
go run ./ -f testdata/Taskfile.go -n Default # dry run: print, don't execute

# or install once, then run from anywhere with zero rebuild
go install                                # into $(go env GOPATH)/bin
task-run -f ./Taskfile.go Default
```

Tasks are exported functions — `func Name()`, `func Name() error`, or
`func Name(a, b string) error`. The doc comment's first line is the `-l`
description. Arg list is `Name:arg1,arg2`.

Taskfiles import the stub `task` package
(`github.com/podhmo/minigo/examples/task-run/task`, or just `task`); the
engine binds every member to a host intrinsic, so the file typechecks under
plain Go tooling but only executes inside `task-run`. Relative paths and
shell commands anchor at the **Taskfile's directory** — the interpreter's
virtual cwd — so the same relative path names the same file no matter
where you invoke the binary from, and a task that runs `os.Chdir` never
moves your shell.

## Dry run (`-n`)

Like `make -n`, `-n` prints what a task would do instead of doing it:

- `task.Sh` / `task.Run` / `task.RunIn` / `task.Output` print the command
  line to stdout. `task.Output` returns `""` and a nil error, so code that
  consumes its result sees an empty value.
- The error-only `os` mutators (`WriteFile`, `Remove`, `RemoveAll`,
  `Mkdir`, `MkdirAll`, `Rename`, `Truncate`) print as `# os.Name args...`
  and return nil.
- `os/exec`: `exec.Command` returns a stand-in whose `Run` / `Start` /
  `Output` / `CombinedOutput` print the command line (prefixed with
  `(in dir)` when `cmd.Dir` is set) instead of spawning it; the output
  methods return no bytes. A command that is built but never run prints
  nothing.

The rest of the Taskfile still runs: `fmt.Println`, `task.Log`, and reads
like `task.Target` / `os.Stat` behave normally, so `-n` shows the path the
tasks take against the current filesystem state. `os.Create` / `os.OpenFile`
(which hand back a file) are not intercepted.

```console
$ task-run -f testdata/Taskfile.go -n Default
linting...
echo 'gofmt ok'
building...
go version
compiler:
# os.WriteFile app.out ... 0644
done
```
