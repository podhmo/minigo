# Experiment: script-side function-body traversal via compiled ops

Status: experiment (draft, not necessarily for merge)

Question (TODO: "Script-side traversal of function bodies"): nothing in
`minigo.dev/inspect` exposes a function decl's body — index/syntax stop
at the signature. What would an inspect body view need to look like?
The experimental driver: infer a net/http handler's request parameters
(OpenAPI-style: `path`/`query`/`header`/`cookie`/`form`/`body`) — the
interesting reads hide inside helper functions, so the view must
support descending into callees, same-package or across an import.

## Verdict

Yes — and the shape that worked is a **compiled op list**, not an AST
mirror. `inspect.Ops(d)` lowers a func/method body (or a func literal)
into ~10 flat instruction kinds — `ref`, `lit`, `sel`, `call`, `bind`,
`ret`, `comp`, `func`, `index`, `expr` — where every op defines a
register and `call` ops carry `Fun`/`Args` registers. Walking those is
enough for a script to reconstruct receiver chains, classify which
values are request-derived, match accessor calls, and re-enter a
callee's op list with its parameter binds mapped to the caller's
tracked arguments. `ret` ops close the loop: a helper that returns the
request (`func pass(r *http.Request) *http.Request { return r }`)
marks the call result as still request-derived.

Two views now exist, one generic and one compiled:

- `inspect.Body(d)` → a `Node` over the `*ast.BlockStmt`;
  `inspect.Nodes(n)` → children in source order, each tagged with the
  `Role` it fills in the parent. This is the generic mirror — needed
  first, and still used for the `BodySmoke` sanity check.
- `inspect.Ops(d)` → `[]*Op`, the compiled form the analyzer actually
  consumes. Param binds (`bind` ops with `Tok == "param"`) lead the
  list, so a callee's formal names line up with the caller's arg
  registers by position — and the same convention makes a `func` op's
  literal body self-describing (`inspect.Ops(funcOp)` returns the
  lit's own ops).

The analyzer ([testdata/inspectbody/main.go](../../testdata/inspectbody/main.go))
is ~560 lines of minigo script and recovers, for every handler shape in
the fixture: `r.PathValue`, `r.URL.Query().Get/Has`, `r.Header.Get`,
`r.Cookie`, `r.FormValue`, `json.NewDecoder(r.Body).Decode(&in)` body
schemas via `Fields`+tags, aliases (`values := r.URL.Query()`),
conversion wrappers (`strconv.Atoi` re-types the inner read), same-
package and cross-package two-level helpers, renamed request
parameters, method handlers (`mux.HandleFunc(..., srv.GetItem)`), and
anonymous `FuncLit` handlers — route patterns bound from
`http.HandleFunc`/`mux.HandleFunc` registrations. Stdlib calls stop at
`Lookup` returning nil — bound packages carry no decl, so the walk is
shallow by construction.

## What `inspect` needed (the point of the experiment)

Ordered by how much the analyzer relies on it:

### 1. `Ops` — the compiled body view

An AST mirror forces a script to interpret ~40 node kinds; the op list
collapses that to registers and roles. Two design choices carried the
weight:

- **`bind`/`ret` ops are the tracking surface.** `x := f()` is a `bind`
  op naming its lhs; `return x` is a `ret` op. A value's derivation
  class ("req"/"query"/"header"/...) is memoized per register and
  propagates through `sel` bases and `bind` names for free — the
  script never re-parses an expression tree.
- **Every op keeps its backing `ast.Node`** (unexported), so the
  existing `SymbolID`/`AsExpr`/`Lookup` apply to ops — identity
  (`strconv.Atoi`), type anchors (`var in CreateUser`), and callee
  resolution all reuse the syntax layer.

### 2. `Lookup` — a non-trapping `Resolve`

A bare-ident callee (`parseFilter(r)`) looks identical to a local var
until you try to resolve it; `Resolve` traps when the symbol doesn't
resolve, so a non-trapping twin was required. This is also what makes
"stop at stdlib" natural: `strconv.Atoi` resolves to a SymbolID (for
the conversion-table lookup) but `Lookup` reports nil — bound packages
have no decl.

### 3. Role/slot labels on the generic `Node` view

The one thing plain `ast` children can't express: which *slot* a child
fills (`ValueSpec`'s single trailing child is its Type or its Value —
`var x T` vs `var x = v`). `Node.Role` fixed that; the compiled view
sidesteps it entirely (`bind.Tok` distinguishes `param`/`var`/`:=`).

### 4. FFI constraints that shaped the API

- Stub functions must return `*runtime.Slice` (`boxedSlice`) — a
  `[]*Op` field or method result stays boxed and isn't rangeable from
  scripts. So `Op.Args`/`Op.Srcs` are `*runtime.Slice` fields (of
  int64s), not `[]int` (`[]int` doesn't auto-unbox; only
  `[]string`/`[]byte` do).
- NIL arguments trap inside intrinsics — scripts must nil-check before
  `AsExpr`/`Nodes`/`Ops`.
- `Decl.Package` member access resolves as the package *namespace*,
  not the struct — `d.Package.Path` reads "member Path of package
  httppkg". `d.Pos` (file:line:col) + `d.Name` serves as the dedup key
  instead.

## What the compiled view deliberately doesn't give you

- **Control flow.** `if`/`for`/`switch` bodies flatten into the op
  stream in source order — over-approximation is the right trade for a
  discovery pass (a read in a never-taken branch still exists).
  Guards/branching would need real CFG ops if a consumer asks.
- **Assignments per-name for multi-return.** `x, y := f()` records one
  src register; per-name results would need tuple unpacking ops.
- **Execution.** Ops describe structure, not evaluation — values are
  classes, not evaluated results.

## Files

- `inspect/node.go` — generic `Node` view (`BodyOf`, `ChildrenOf`,
  `AsExprOf`)
- `inspect/ops.go` — the compiler (`OpsOf`, `Op`, op kinds)
- `inspect.go` — intrinsics `Body`/`Nodes`/`AsExpr`/`Ops`/`Lookup`;
  `SymbolID`/`Resolve`/`AsExpr` accept ops and nodes alike via the
  shared `astBackedView` seam
- `inspect/stub.go` — script-visible signatures
- `testdata/httppkg`, `testdata/httphelp` — handler + cross-package
  helper fixtures
- `testdata/inspectbody` — the analyzer script (the deliverable: proof
  the surface is sufficient)
- `inspect_body_test.go` — asserts the canonical inferred table
