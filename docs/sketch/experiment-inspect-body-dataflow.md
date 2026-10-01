# Experiment: function-body traversal via `inspect.Ops` (compiled-op dataflow)

Status: experiment (draft, not necessarily for merge)

Question from TODO.md: nothing in `minigo.dev/inspect` exposes a function
decl's body — index/syntax stop at the signature. Can a script walk a
body well enough to do real analysis? Test subject, per the task:
inferring net/http request parameters (OpenAPI-style) from handler
bodies, where reads may live inside helper functions.

The user's hint — "compile bodies to a special VM code and track call
arguments/return values" — is taken literally: the experiment reuses the
interpreter's *own* compiled bytecode (`compile.Func` → `bytecode.Chunk`)
and adds a host-side *lifting* pass that turns the stack-machine stream
into a flat op list with explicit value ids. That lifted form is the
"special VM code" scripts can analyse without simulating a stack.

## Verdict

Yes, and the whole analysis lives on the script side. `inspect.Ops(decl)`
returns a `*Body` — a flat `[]*Op` where every value-producing op carries
`Out`/`NOut` value ids and `Ins` consumed ids, calls carry `Fun`/`Args`,
callee/global ops carry a resolved `Sym`, coerce ops carry the declared
`Typ`, and func literals carry a nested `Body`. The test analyzer
(`testdata/inspectbody`) rebuilds the route table of `testdata/inspectapp`
— including reads inside same-package helpers, cross-package helpers,
method handlers, func-literal handlers, aliases, two-level indirection,
and a request-returning passthrough — with one table, five routes, all
parameters recovered (see `inspect_body_test.go` for the golden string).

## What `inspect` needed

In order of how much the analyzer leaned on each piece:

1. **`Op.Out`/`Ins` value ids (dataflow, not stack).** Every consumer
   wants "where did this call argument come from" — on raw bytecode that
   is a stack simulation. The lift does it once, host-side, and scripts
   get a def-use graph: `def[v]` → producing op, `op.Ins` → inputs. This
   is the single thing that makes the rest possible.

2. **`Op.Sym` — resolved callee identity without a typechecker.** A
   `pkg.Fn(...)` call resolves to `{import path, name}` at lift time via
   the file's import scope (`pkg.Scopes[file]`), same as `trySpecial`.
   Same-package calls get `{pkg.Path, name}` so `inspect.Symbol`-style
   lookup reaches them; stdlib/bound members resolve to paths that never
   have source decls — which is *exactly* the shallow-interpretation
   boundary (see below).

3. **`Op.Typ` — declared types at binding sites.** `var p Payload` emits
   a coerce op whose typedef value resolves back to the type expression —
   `json.NewDecoder(r.Body).Decode(&p)` gets its body schema from `&p`'s
   slot without any typechecking. Params carry their declared `Type`
   directly (`Body.Params`), which is how `*http.Request` parameters get
   their "request" class and how `srv.DeleteItem` (a method value)
   resolves its receiver type.

4. **`Op.Body` — nested lifted bodies on `func` ops.** `OpMakeClosure`
   stores the callee `*runtime.Function` (chunk + synthesized decl); the
   lifter lifts it eagerly, so `mux.HandleFunc("POST /echo/{word}",
   func(w, r){...})` is reachable the same way a named handler is.

5. **`Op.Text`/`Pos` — best-effort source rendering.** Read
   classification is suffix-matching on expression chains
   (`...Query().Get`), and every node in a call chain shares the head's
   `Pos`, so the lifter picks the AST node by kind-preference at the
   shared position and renders it. `Text` is a debugging aid; the
   analyzer only needed `Name`/`Sym`/`Ins`, but the rendered text was
   invaluable while developing it.

6. **`NOut`/`Slot`/`Tok` fidelity.** `unpack` (multi-assign), `range`
   (per-iteration yields), `NamedSlots` bare returns — the lift tracks
   all of them so `ret` ops have honest `Ins` even for named-result
   functions. `ret` inside a helper is how return-value tracking works:
   the callee's `ret` ids whose values are request-derived mark the call
   result request-derived (that's `apputil.ReqOf(r).URL.Query().Get(...)`).

## What stopped where (the shallow boundary)

The analyzer descends into a call only when the callee resolves to a
package *inside the analyzed module* — a module-prefix rule derived from
the package's own `Path` vs `Dir`. That choice is load-bearing:

- **Bound packages** (`strconv`, `encoding/json`, `net/url`) have no
  `Decls` — descent stops with no special casing.
- **Unbound stdlib** (`net/http` — *not* bound in this engine!) would
  happily load from GOROOT and descend into `http.HandleFunc`'s real
  body, then hit imports outside the module. The module gate prevents
  even attempting the load. Finding: `inspect.PackageOf` on an
  unresolvable path is an uncatchable `*Trap`, not a recoverable
  `*Panic` — "can I load this?" has to be answered without probing.
- **Unresolvable callees** (method values on interface-typed receivers,
  dynamic dispatch) get `Sym == ""` and no body — descent just skips.
  Linear lifting ignores jump targets (a *may*-analysis: branches union,
  not paths), which is the right semantics for pattern detection and the
  wrong one for typestate-style checks — that limitation is inherent to
  reading an op list rather than interpreting it.

## Surprises the experiment surfaced

- **`OpInstantiate` doubles as indexing.** `m[k]` compiles to the same
  opcode as `F[T]` (the VM falls back to index lookup for non-generic
  bases). The lift reports it as `inst`; consumers that care about
  `base[key]` reads must treat `inst` like `index`. This is an encoding
  quirk that a body API *has* to document.
- **The param-name literal never survives a helper boundary on its own.**
  `localQuery(r, "sort")` only works because the callee's param value
  seeds the argument's literal and chain shape — per-frame `lit`/`shape`
  maps keyed by param value id. `q.Get` on a `url.Values` parameter is
  recognizable only through that inherited shape (the chain text is just
  `q.Get`).
- **Positions collapse inside call chains.** `r.URL.Query().Get` — every
  node has `Pos == r.Pos()`. `Text` recovery needs kind-aware AST
  picking, not position lookup.
- **No new engine machinery was needed beyond the lifter.** `Ops` is a
  thin intrinsic: `declViewOf` → `compile.Func` → lift → `GoValue`. The
  analysis (taint, read patterns, route extraction) is ~600 lines of
  ordinary minigo in `testdata/inspectbody/main.go`.

## Limits (deliberate or discovered)

- May-analysis only: `if`/`else` reads are unioned; both sides appear.
- Element/class tracking stops at composite boundaries (a map of
  requests loses per-element taint).
- `eval` (OpEvalAST) is opaque — quoted/late-compiled fragments are
  reachable only via their `ASTFragment`, which the lift attaches as
  `expr` but does not inline.
- Variadic spreads, method calls through interface values, and
  `reflect`-driven call sites are out of scope by construction.
- Cross-*module* (third-party dep) helpers would descend too — the gate
  is module-boundary, not app-boundary. For this subject there were none.

## Answer to the TODO item

"An inspect body view (statement/expr children, decl-anchored
TypeExprs)" — the experiment suggests the *op-dataflow* form is the more
useful primitive: the script gets def-use edges, callee identity, and
declared types, while staying shallow (one function's op list; descent
is the script's choice). A pure statement/expr tree would have needed a
script-side def-use pass anyway. `inspect.Ops` plus the existing
`Symbol`/`Decls`/`Methods`/`UnRef`/`SymbolID` intrinsics were
sufficient — no other inspect additions were required.

## Layout

- `inspect/body.go` — the `Body`/`Op`/`Param` views and the lifter
  (`OpsOf`), ~900 lines; per-op stack-effect table in `step`.
- `inspect/stub.go` + `inspect.go` — the `inspect.Ops` intrinsic.
- `testdata/inspectapp/` (+`apputil/`) — the handler fixture.
- `testdata/inspectbody/main.go` — the analyzer script.
- `inspect_body_test.go` — the golden route table.
