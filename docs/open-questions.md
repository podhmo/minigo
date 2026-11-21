# Open questions

Decisions the working sessions could not settle on their own. One entry
per question: what is blocked, the options, and who decides.

- **Should a parent's explicit `Bind` of a stdlib path propagate to `NewSession`?** A session builds its own lazy tables, and paths the session still holds pending skip the parent's copy — so `e.Bind("os", custom)` followed by `e.NewSession()` gives the session the default `os` table, not the custom one. This predates lazy install: under eager install the session bound stdlib itself at construction, so the parent's re-Bind never reached it either. But the `NewSession` doc says inherited host bindings "resolve identically", which arguably covers a custom re-Bind of a stdlib path. Fixing it needs the registry to track explicit-vs-default binds so the session prefers the parent's custom table. (Escalated from Devin Review on [#1150](https://github.com/podhmo/minigo/pull/1150).)

## M4-4: do `runtime.HostNamedIs` / `runtime.HostTwinOf` count as removable twin helpers? (resolved)

Resolved 2026-11-19 (user): both stay as the residual reflect.Type↔typedef
fallback — a two-stage capability injection to remove them was judged
too complex to be worth it. Their removal is tracked as a separate
task in docs/next/ja/TODO.md ("intern 表を拡張して HostNamedIs/HostTwinOf
を除去する"), to revisit once the intern table covers un-interned and
stamp-less typedefs.

## src-netip: can a bound-mode-only stdlib package ever run under `--src`?

`ModeSource` loading of `net/netip` traps at package init:
`unique.Make(addrDetail{})` (netip.go:69) calls `unique.Make[T]` — a
generic builtin whose `T` is the unexported `addrDetail` — which lowers
to `abi.TypeFor` (a compiler intrinsic) plus `unsafe.Pointer(&a)`
reinterpretation. None of that is bindable or source-interpretable:
the intrinsic has no body to run, and the unsafe aliasing defeats the
interpreter's value model.

Options:

- Give the interpreter its own intrinsics for `unique.Make`/`abi.TypeFor`
  (a per-package escape hatch, like asmimpl but for generics).
- Declare `net/netip` bound-only by policy and document that `--src`
  for it is unsupported (the bound path already works).
- A general `unsafe.Pointer` story for source mode — much larger scope;
  pointer-reinterpret casts have no interpreter model.

Decision: needs podhmo. `--src net/netip` reproduces at
`unique.Make(addrDetail{})` — bound `netip` stays the parity path.

## readfile-mem: which slice model closes the os.ReadFile memory gap?

A script `[]byte` stores one 16-byte `runtime.Value` per element, so a
32 MB `os.ReadFile` peaks near 560 MB RSS (issue #950). Two candidate
designs, each with real cost:

- Compact `[]byte` backed by the host `[]byte`, materializing element
  cells only on script writes — needs lazy element ops at ~280
  `.Elems` sites (index, len, cap, append, copy, range, spread).
- Boxing results as `*runtime.GoValue` and adding a `string(b)` fast
  path — boxing breaks `b...`, `copy`, `[]byte(b)`, and `slices.*`
  (each needs its own special case; the gap keeps widening).

Decision: needs podhmo — it is a memory-model tradeoff, not a local fix.
