# Plan: `minigo repl`

Status: describes what is implemented (PR stack #429–#440 plus the
review pass on top). Open items live in [TODO.md](../../TODO.md).

Goal: an interactive session for **adjusting** Go code. You start the REPL
to poke at a package, try a function, or patch something until it
behaves. When the REPL cannot express something, the fallback is to edit
the source and start again. That stance drives most decisions below. Each
input should do the obviously useful thing, *say* when it overrides
something, and never leave the session half-changed after a failure.

Related documents:

- [plan-package-introspection.md](./plan-package-introspection.md) — the
  `inspect` machinery behind `:cd`/`:ls` and the `:pin` write mode (the
  "REPL: implicit access" and "`:cd` write-mode" sections).
- [ja/experiment-repl-completion.md](./ja/experiment-repl-completion.md) —
  completion (`(*REPL).Complete`, TAB), including a comparison with gore,
  yaegi and IPython. This document does not repeat that comparison. Other
  REPLs come up below only where they explain a decision.

## Execution model

A session is one scratch package, `<repl>`, owned by a fresh session
engine (`Engine.NewREPL`). Its source is never a real file. The REPL
keeps the accepted inputs as text and **rebuilds** the package from them
after every input (`reload`).

```
<repl> package
├── repl.go        prompt file: imports + accepted decls + generated steps
├── /abs/fib.go    :load-ed files, one syntax.File each (own import scope)
└── ...
```

- **Classification.** `EvalLine` first parses the input as top-level
  declarations (`package repl\n` + input). If that fails, it parses the
  input as statements inside a function body.
- **Declarations** (func, type, method) are appended to the prompt file as
  text. Redefining a name appends a newer decl, and the index's
  last-wins maps make it the one that resolves.
- **Statements** become a generated step, `func __stepN() any { ... }`.
  The step runs once and stays in the source, so the package keeps
  parsing the same way. A trailing expression statement becomes the
  step's return value, and the REPL prints it.
- **Result variables.** The last three printed results are kept as `_1`
  (newest), `_2` and `_3`, the counterpart of IPython's `_`, `__`, `___`
  (Go's `_` is the blank identifier and cannot be read). A multi-value
  result has no Go spelling as a tuple, so it is stored as a `[]any`:
  `strconv.Atoi("42")` then `_1[0]`. Inputs that print nothing, and
  failing inputs, leave them unchanged. A user binding named `_1` is
  overwritten by the next result.
- **Hoisting.** Names introduced by `x := e`, `var` and `const` are
  promoted to package-global cells (`hoist`), so they persist across
  lines. `x := e` reuses an existing global instead of shadowing it. This
  is a deliberate divergence from Go, in the style of Python REPLs. A
  `const` cell is sealed read-only after its step runs. A typed
  `var x T` cell is stamped with T so later stores coerce.
- **Const groups** follow Go. A value-less spec repeats the previous
  spec's type and values. `iota` is the spec index, bound by wrapping
  each const spec's assignment in `{ const iota = i; ... }`.
- **Reload** re-parses the prompt text, adds the cached loaded files,
  rebuilds the index and per-file import scopes, and evicts cached
  `Function`/`TypeDef` values so redefinitions take effect. Cells are
  never evicted by a reload: they belong either to the prompt (hoisted
  names) or to a `:load` unit, and their owner manages them.

### Failure leaves the session unchanged

Every input is transactional:

- A failing decl input rolls back its source, the cells it hoisted, the
  consts it redeclared, and any loaded decls it shadowed
  (`rollbackSource`).
- A failing step keeps the decls that came with it, as in Python, but
  drops the cells it created and restores the consts it redeclared.
- A failing `:load` restores the units, the prompt decls, the `:pin`
  bookkeeping and the global bindings of every name it touched.
  Initializers bind fresh cells, so restoring the bindings is enough.

## The newest definition wins

The one rule for collisions: **a later definition replaces an earlier one,
whatever produced either.** Sources of definitions are prompt decls,
prompt `:=`/`var`/`const`, `:load`, and `:pin` publications.

| Earlier | Later | Result |
|---|---|---|
| prompt `func F` | prompt `func F` | the new body |
| prompt `func F` / `F := 1` | `:load` declaring `F` | the file's `F` (prompt decl/cell removed) |
| loaded `F` (func, type, var, const) | prompt `func F` / `type F` | the prompt's (file decl shadowed until the next `:load`) |
| loaded `var x` | prompt `x := v` | an assignment to the loaded var (same cell) |
| `const C` | `const C` / `var C` / `C :=` | a new binding, **with a warning** |
| `const C` | `C = v` | trap: `cannot assign to constant`, as in Go |

Only the names a later definition actually declares are affected.
`y := 20; :load g.go` leaves `y` alone if `g.go` does not declare `y`.

**Warnings.** A redefinition that is easy to miss is reported through
`REPL.Warnings()`, and the CLI prints it as `warning: ...`. Today this
covers consts:

- `const C redeclared (was 10)`
- `const C redeclared by c.go (was 30)`
- under `:pin`: `const K redeclared in package <path> (was 1): every importer sees the new value`

A failed input produces no warning. Re-loading a file over the const that
the same file bound earlier is silent.

## Imports

- Imports are ordinary Go syntax at the prompt and are **resolved
  eagerly**. A typo or a path outside the module's requires fails the
  import line, not the first use. Locating and indexing happen at import
  time; initializers still run on first use.
- An unaliased import binds the package's **declared** name.
  `testdata/oddname` declares `oddpkg`, so the spec is rewritten to
  `oddpkg "…/oddname"`. Bound (native) packages have no package clause and
  keep Go's path-derived name, so `example.com/foo/v2` binds `foo`.
- Directory imports (`import "./dir"`, `"/abs/dir"`) are a REPL extension.
  They resolve against the engine's start directory, not the host
  process's cwd.
- Names a session import binds can be used as refs by meta-commands
  (`:ls foo`, `:cd foo`, `:doc foo.Unmarshal`) via `REPL.ImportPathOf`.

## `:load <file|dir>`

Reads a `.go` file, or a directory's buildable files (go/build match
rules, `_test.go` excluded), **into** `<repl>`. It is not `:cd`:

| | `:cd ./foo` | `:load ./foo` |
|---|---|---|
| what | enter an existing package (name resolution changes) | copy the files' decls into the session |
| owner of the decls | the package, shared with every importer | the session (redefinable, gone on `:reset`) |
| unexported names | reachable while inside | reachable (same package block) |
| single file | n/a (package granularity) | `:load ./fib.go` |
| `package main` | n/a | `func main` is defined, not run |
| leave | `:cd -` | `:reset` |

This is GHCi's `:load` rather than an alias of `:cd`. The use case is the
edit–load–call loop: `:load "./fib.go"; Fib(10)`, edit, `:load` again.

Semantics:

- **The package block is shared; imports are per file.** Loaded decls
  join the package block, so the prompt calls exported and unexported
  names alike and can extend them (`type XX X`, new methods on `X`). Each
  file keeps its own import scope, as the files of one Go package do.
  `import x "strings"` in a.go and `import x "bytes"` in b.go coexist, and
  the prompt does not see a file's imports. A relative import inside a
  loaded file anchors at that file's directory. The package clause is
  ignored.
- **Initialization.** Var/const initializers run in cross-file dependency
  order (`compile.InitFunc` over an index view restricted to the unit),
  then `init()`. This happens once per load.
- **Duplicates are errors, as in Go.** A name declared twice within a load
  (in the same file or across files) rejects the load with positions:
  `f.go:4:1: F redeclared (previous declaration at f.go:2:1)`. So does a
  name another load already owns.
- **Re-load replaces.** `:load` of the same path (any spelling: `f.go`,
  `./f.go`, absolute) replaces what it loaded before, so decls dropped
  from the file become undefined. A directory load supersedes single-file
  loads of its own files. Loading one file of an already-loaded directory
  is refused with a hint to reload the directory.
- **Paths only.** `:load strings` is an error that points at `import` and
  `:cd`. Copying a library package into the session would blur the line
  with `:cd`.
- `:load` with no argument lists the loaded refs.

Implementation: `repl_load.go`. A `loadUnit` holds the parsed files
(parsed once into the engine FileSet), the decl keys, the cells its
initializers bound (ownership), and the keys shadowed by later prompt
decls. Shadowed decls are dropped from a copy of the file AST at reload.

## `:pin`: monkey-patching an entered package

`:cd <pkg>` then `:pin` publishes subsequent declarations into the
entered package's globals (design in plan-package-introspection.md).
Redeclaring a package const under `:pin` replaces it for every importer.
This is deliberate, since adjusting a package is what the REPL is for,
and the warning names the package. `:load` under `:pin` replaces a
published decl's repl binding like any other prompt decl. The package
keeps the published patch.

## Meta-commands

| command | purpose |
|---|---|
| `:help` | command list |
| `:reset` | fresh session (definitions, values, imports, loads) |
| `:cd <ref>` / `:cd` / `:cd -` | enter a package / show it / leave |
| `:pin` / `:unpin` | write mode for the entered package |
| `:ls [ref]` | top-level decls (`func`/`type`/`var`/`const`/`method`, bound symbols as `host`); ref = path, `./dir`, quoted path, or a session import name |
| `:load [<file\|dir>]` | read files into the session / list loads |
| `:doc <pkg>[.<sym>]` | run the Go toolchain's `go doc` |
| `:comp <text>` | print completion candidates (what TAB offers) |
| `:bindings [prefix]` | list host-bound (native) import paths |
| `:exit` / `:quit` / `:q` | quit |

Notes:

- `:doc` shows **Go's** documentation, not minigo's. A bound package may
  expose fewer symbols than go doc lists. `:ls <pkg>` shows what minigo
  actually provides. Directory packages run `go doc . Sym` inside the
  directory so their module supplies the context.
- `:bindings` lists the paths importable without interpreting source
  (`Engine.BoundPackages`). Paths outside the list are interpreted from
  source and may hit unsupported constructs. `Engine.BoundSymbols` is the
  host-side accessor for a bound package's symbols.
- On a terminal, the line editor completes code and `:command` names with
  TAB and keeps `~/.minigo_history`.

## Known approximations

These are tracked in TODO.md:

- `:load`:
  - A loaded var that took over a prompt var does not revert to the prompt
    value when it is later removed from the file.
  - A grouped prompt `type (...)` is dropped whole when a load declares
    one of its names.
  - A prompt decl taking over one name of a multi-name var/const spec
    drops the whole spec. Dropping a spec from a const group shifts
    `iota` and implicit repetition for the specs after it.
  - There is no unload short of `:reset`.
  - `:ls` does not show a decl's source file.
- A failing step keeps the decls of its input (Python-like), so a
  shadowing mark from such an input stays too.
- Completion gaps (call-expression bases, string-context arguments such
  as `:load ./fi`+TAB) are listed under "REPL completion" in TODO.md.
