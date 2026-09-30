# task-run — a mage-style task runner on minigo

A task runner (in the spirit of [mage](https://magefile.org) /
[go-task/task](https://taskfile.dev)) where the Taskfile is a Go file
interpreted by [`minigo`](../../). See
[sketch/plan-task-runner.md](../../sketch/plan-task-runner.md) for the design.

## Usage

```sh
# one-off (compiles via the build cache each call)
go run ./ -f testdata/Taskfile.go -l      # list tasks (name + doc comment)
go run ./ -f testdata/Taskfile.go Default # run a task
go run ./ -f testdata/Taskfile.go Greet:world Clean

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
