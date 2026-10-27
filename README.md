# minigo

An embeddable interpreter for a large subset of Go, compiled to bytecode
and executed on a lazy stack VM — packages are located, indexed, and
initialized on demand as the script's execution reaches them.

```go
e := minigo.NewEngine(".")
v, err := e.Run(ctx, "./script", "Main") // dir, file, or import path + entry func
```

- lazy per-function compilation: packages are indexed up front, each
  function is compiled to bytecode only when execution reaches it
- scripts are valid Go, verifiable with `gopls`/`go vet`; the compiler
  still never fails — unimplemented constructs emit a trap that fires
  only if execution reaches them, so everything else still runs
- special forms ("quoted Go"): ordinary Go calls dispatch by canonical
  symbol identity to host handlers that receive the call's syntax
  instead of evaluating it — stub packages keep DSL scripts typed and
  gopls-friendly (`examples/convert-define`, `examples/task-run`)
- host boundary: Go values box with reflective method dispatch; stdlib
  intrinsics (`fmt`, `strings`, `os`, `encoding/…`) bind as packages
- generics: monomorphize-on-use, including the Go 1.26/1.27 deltas —
  `new(expr)`, self-referential constraints, generic methods,
  promoted-field literal keys, generalized func-type inference
- introspection: `minigo/inspect` exposes packages, decls, type
  expressions and values to scripts; the REPL has `:cd`/`:ls`/`:pin`
- speed is a non-goal: the lazy-VM design favors implementation
  simplicity and coverage over runtime performance
- concurrency, best-effort: `go` statements, channels, and `select`
  run on host goroutines — enough for typical scripts to work, not a
  faithful model of Go's concurrency; see `TODO.md` for the gaps

A `minigo` CLI is included (`./cmd/minigo`): `minigo run <ref> [--entry F]`,
`minigo repl`, `minigo vet <ref>`, `minigo gen-intrinsics`, or the
`minigo <ref> [func]` shorthand (`minigo help [command]` or `-h` prints
usage). See `docs/sketch/plan-minigo-vm.md` for the
design, `TODO.md` for current coverage and remaining gaps, and
`examples/task-run` for an embedding example and `examples/convert-define`
for a DSL tool built on special forms.

## REPL

`minigo repl` is an interactive session over a scratch `<repl>` package.
Each line is a top-level declaration or a statement list; a trailing
expression prints its value. Names introduced by `:=`, `var`, and `const`
persist as globals across lines.

```
>> import "fmt"
>> fmt.Println("hello")
hello
(6, <nil>)
>> import "./sub"
>> sub.Greet("bob")
hi bob
```

Imports are ordinary Go syntax — `import "path"`, `import name "path"`,
`import . "path"`, `import _ "path"` — resolved by the same module rules
as `minigo run` (below), plus `./dir`/`/abs/dir` conveniences that load a
directory eagerly and bind the package's declared name (`import "./odd"`
on a dir declaring `package oddpkg` binds `oddpkg`).

Inside the session, `:help` lists the meta commands: `:cd <ref>` /
`:cd -` scope-switches into a package (unexported names included),
`:ls [ref]` lists top-level decls, `:pin` / `:unpin` monkey-patch the
entered package's globals, `:reset` clears the session, `:exit` quits.

## Import resolution

Package loading follows the Go module on disk — there is no `go list` and
no downloading. `NewEngine(startDir)` walks up from `startDir` to the
enclosing `go.mod` (or `go.work`) and resolves each import as:

1. `replace` directives in that `go.mod`
2. the current module's own path
3. `GOROOT` for the standard library
4. `go.mod` `require` entries, mapped into `GOMODCACHE`
   (`go mod download` must have populated it already)

The module context is fixed at engine creation, so run minigo from
inside the target module — e.g. `cd mymodule && minigo run .` — not from
a different module's tree. External module source is interpreted like
any other package; what still traps is cgo and `unsafe`-dependent
internals (e.g. `reflect`'s `internal/abi` init).

### Currently unsupported

- **cgo** — `import "C"` is rejected at load
  (`cgo is not supported: import "C"`)
- **`unsafe.Pointer` reinterpretation** — anything that needs
  `unsafe.Pointer`/`SliceHeader` tricks traps (e.g. go-toml's
  `internal/danger`, `reflect`'s `internal/abi` internals)
- **script-declared types inside host code** — a host function that takes
  `any` (text/template's `Execute` data, say) cannot see a type the
  script declares, because reflect cannot mint one: a `type N int` value
  arrives as its basic type (`int`), a slice of script structs as
  `[]interface {}`, and a script struct as a field/method map, so
  host-side `%T`, `{{.}}` and the script type's own methods (`String`)
  do not apply. Basics, bound host types and slices/arrays/maps of them
  keep their Go type. minigo's own intrinsics (`fmt`, `reflect`,
  `encoding/json`, ...) see the script type as usual

Smaller gaps shift as work lands — see `TODO.md` for the live list.

### Performance notes

Speed is a non-goal, but one pattern is worth knowing: a script `[]byte`
holds one boxed value per byte, so reading a large file with
`os.ReadFile` and converting it with `string(b)` costs far more than in
Go (a 32MB file: ~120ms and ~560MB RSS). When the file is only used as
text, copy it into a `strings.Builder` instead — host readers and
writers stay on the host, so this runs at Go speed:

```go
f, err := os.Open(path)
if err != nil {
	return err
}
defer f.Close()
var sb strings.Builder
if _, err := io.Copy(&sb, f); err != nil {
	return err
}
text := sb.String()
```

The same holds for `bytes.Buffer`, `bufio`, `fmt.Fprint*` and the other
`io` helpers when both ends are host values.

## Provenance

This repository is a reboot of [`minigo2/`](https://github.com/podhmo/go-scan/tree/main/minigo2)
from `github.com/podhmo/go-scan` (main @ `87cffe1`). It was copied file-wise
(no git history) and renamed `minigo2` → `minigo`; the only carried
dependency is a vendored subset of go-scan's `locator` in `pkg/` (see
`pkg/SOURCE.md`). The earlier tree-walking interpreter that used to live
here was removed.
