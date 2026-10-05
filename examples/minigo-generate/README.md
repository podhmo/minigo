# Interpreted //minigo:generate (`examples/minigo-generate`)

A `go generate`-shaped runner that never builds or execs a tool
(issue #390). A `//minigo:generate <ref> [args...]` comment names a
package directory holding `func Main(args []string) int`; the runner
invokes it inside one shared minigo engine — no process spawn, no
build cost, and the engine's package caches stay warm across
directives. The marker is deliberately its own word: `//go:generate`
lines keep going to `go generate`; a file opts individual tools into
interpreted execution.

It is the dual of `gen-sync`: that example *writes* `//go:generate`
directives it never runs; this one *reads and runs* directives it never
writes. See `docs/sketch/plan-minigo-generate.md` for the design note.

## How it works

```console
$ go run ./ ./app       # scan ./app, run every directive
$ go run ./ -x ./app    # echo each directive as it runs
$ go run ./ -n ./app    # dry run: print directives, run nothing
$ go run ./ -run=Priority ./app   # only directives matching a regexp
```

The host (`main.go` + `scan.go`) does all of it in-process:

1. **Scan** — `syntax.ParseFile` the dir's `.go` files and read the raw
   comment table for `//minigo:generate` lines (they are Go directive
   comments, so `CommentGroup.Text()` would strip them like
   `//go:generate`). Filename order, then position order.
2. **Execute** — per directive: resolve the ref against the file's
   directory, swap `GOFILE`/`GOFILEPATH`/`GOLINE`/`GOPACKAGE` into the
   env, and `Engine.Run` the tool package's `Main`. Each `Engine.Call`
   is an isolated process scope — a tool's panic dies with its call —
   while the shared package cache parses and indexes each package once.
   Failures are reported per directive and the run exits 1.

## The tool contract

```go
func Main(args []string) int
```

`args` is the directive's arguments — `argv[1:]` of the equivalent
command. The file context arrives through the environment, so a tool
reads `os.Getenv("GOFILEPATH")` exactly as it would under `go generate`.
`Main` also means the same source doubles as a real command:

```go
func main() { os.Exit(Main(os.Args[1:])) }   // tools/stringer
```

## Demo

`tools/stringer` is a String() generator written on `inspect` — the
package that answers "which declarations live in this package"
(`DirOf`/`Files`/`Decls`/`EnumMembers`/`Def`). No `go/types`, no
`go/packages`, no binary: it finds the `-type` in `GOFILEPATH`'s
package, switches on its enum consts, and writes
`<type>_string.go` next to the source. `app/` seeds three enums plus
decoys — an alias wearing a matchable name, a const-less named int, a
directive that trails its type instead of preceding it.

```console
$ go run ./ -x ./app
app/models.go:3: ../tools/stringer -type=Status
stringer: wrote .../app/status_string.go
app/models.go:12: ../tools/stringer -type=Priority
stringer: wrote .../app/priority_string.go
app/decoys.go:17: ../tools/stringer -type=Level
stringer: wrote .../app/level_string.go
```

`make demo` runs it twice to show regeneration is stable; `make clean`
removes the generated files (they are untracked — output, not fixture).

## Layout

- `main.go` — flags, the engine loop, per-directive env swap + `e.Run`
- `scan.go` — `//minigo:generate` discovery on the raw comment table
- `tools/stringer/` — the interpreted tool: `package main` with a
  `Main(args []string) int` body, dual-mode as a real command
- `app/` — the scanned fixture: enums to generate, decoys to ignore
- `testdata/` — expected generated files asserted by `main_test.go`
